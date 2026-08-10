package studio

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ginkida/gokin-studio/internal/engine/security"
)

const (
	previewConfigMaxBytes = 256 << 10
	previewLogMaxBytes    = 256 << 10
	previewMaxConfigs     = 8
	previewMaxArgs        = 48
	previewReadyTimeout   = 25 * time.Second
)

type PreviewServerConfiguration struct {
	Name              string            `json:"name"`
	RuntimeExecutable string            `json:"runtimeExecutable"`
	RuntimeArgs       []string          `json:"runtimeArgs,omitempty"`
	Port              int               `json:"port"`
	Command           string            `json:"command"`
	Cwd               string            `json:"cwd,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	AutoPort          *bool             `json:"autoPort,omitempty"`
	Program           string            `json:"program,omitempty"`
	Args              []string          `json:"args,omitempty"`
	URL               string            `json:"url,omitempty"`
}

type SessionPreviewConfig struct {
	Version        string                       `json:"version"`
	AutoVerify     bool                         `json:"autoVerify"`
	Configurations []PreviewServerConfiguration `json:"configurations"`
	Source         string                       `json:"source"` // file / detected / none
	Path           string                       `json:"path"`
}

type previewConfigEntry struct {
	Name              string            `json:"name"`
	RuntimeExecutable string            `json:"runtimeExecutable"`
	RuntimeArgs       []string          `json:"runtimeArgs"`
	Port              int               `json:"port"`
	Cwd               string            `json:"cwd"`
	Env               map[string]string `json:"env"`
	AutoPort          *bool             `json:"autoPort"`
	Program           string            `json:"program"`
	Args              []string          `json:"args"`
	URL               string            `json:"url"`
}

type previewConfigFile struct {
	Version        string               `json:"version"`
	AutoVerify     *bool                `json:"autoVerify"`
	Configurations []previewConfigEntry `json:"configurations"`
}

type PreviewServerStatus struct {
	Configuration string `json:"configuration"`
	State         string `json:"state"` // stopped / starting / running / failed
	URL           string `json:"url,omitempty"`
	BrowserURL    string `json:"browserURL,omitempty"`
	Port          int    `json:"port,omitempty"`
	PID           int    `json:"pid,omitempty"`
	StartedAt     int64  `json:"startedAt,omitempty"`
	Logs          string `json:"logs,omitempty"`
	Error         string `json:"error,omitempty"`
	BridgeToken   string `json:"bridgeToken,omitempty"`
}

type previewServerRun struct {
	mu                sync.RWMutex
	projectID         string
	sessionID         string
	config            PreviewServerConfiguration
	cancel            context.CancelFunc
	cmd               *exec.Cmd
	state             string
	startedAt         int64
	logs              previewLogBuffer
	err               string
	proxy             *http.Server
	proxyURL          string
	browserURL        string
	targetURL         string
	bridgeToken       string
	autoVerify        bool
	profile           *previewSessionProfileState
	staticPath        string
	staticRoot        *os.Root
	staticTarget      *http.Server
	staticAccessToken string
}

type previewLogBuffer struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (b *previewLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(p) >= previewLogMaxBytes {
		b.data = append(b.data[:0], p[len(p)-previewLogMaxBytes:]...)
		b.truncated = true
		return len(p), nil
	}
	if excess := len(b.data) + len(p) - previewLogMaxBytes; excess > 0 {
		copy(b.data, b.data[excess:])
		b.data = b.data[:len(b.data)-excess]
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *previewLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	text := string(b.data)
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
	}
	if b.truncated {
		return "[earlier output truncated]\n" + text
	}
	return text
}

func previewServerKey(projectID, sessionID, configuration string) string {
	return projectID + "\x00" + sessionID + "\x00" + configuration
}

func (s *Studio) GetSessionPreviewConfig(projectID, sessionID string) (*SessionPreviewConfig, error) {
	p, session, err := s.projectSession(projectID, sessionID)
	if err != nil {
		return nil, err
	}
	dir, err := sessionWorkingDirectory(p, session)
	if err != nil {
		return nil, err
	}
	return loadSessionPreviewConfig(dir)
}

func loadSessionPreviewConfig(dir string) (*SessionPreviewConfig, error) {
	result := &SessionPreviewConfig{
		Version:    "0.0.1",
		AutoVerify: true,
		Source:     "none",
		Path:       ".claude/launch.json",
	}
	data, _, err := readProjectRegularFile(dir, result.Path, previewConfigMaxBytes)
	if err != nil {
		if os.IsNotExist(err) {
			if detected := detectPreviewConfiguration(dir); detected != nil {
				result.Configurations = []PreviewServerConfiguration{*detected}
				result.Source = "detected"
			}
			return result, nil
		}
		return nil, fmt.Errorf("read %s: %w", result.Path, err)
	}
	clean, err := stripJSONComments(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", result.Path, err)
	}
	var file previewConfigFile
	decoder := json.NewDecoder(bytes.NewReader(clean))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", result.Path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("parse %s: multiple JSON values are not allowed", result.Path)
	}
	if file.Version != "" && file.Version != "0.0.1" {
		return nil, fmt.Errorf("unsupported preview config version %q", file.Version)
	}
	if len(file.Configurations) > previewMaxConfigs {
		return nil, fmt.Errorf("preview config has more than %d configurations", previewMaxConfigs)
	}
	if file.AutoVerify != nil {
		result.AutoVerify = *file.AutoVerify
	}
	result.Source = "file"
	names := make(map[string]bool)
	for _, raw := range file.Configurations {
		config, err := validatePreviewConfigEntry(raw)
		if err != nil {
			return nil, err
		}
		if names[config.Name] {
			return nil, fmt.Errorf("duplicate preview configuration %q", config.Name)
		}
		names[config.Name] = true
		result.Configurations = append(result.Configurations, config)
	}
	return result, nil
}

func validatePreviewConfiguration(name, executable string, args []string, port int) (PreviewServerConfiguration, error) {
	return validatePreviewConfigEntry(previewConfigEntry{Name: name, RuntimeExecutable: executable, RuntimeArgs: args, Port: port})
}

func validatePreviewConfigEntry(raw previewConfigEntry) (PreviewServerConfiguration, error) {
	name, executable, args, port := raw.Name, raw.RuntimeExecutable, raw.RuntimeArgs, raw.Port
	name = strings.TrimSpace(name)
	executable = strings.TrimSpace(executable)
	if name == "" || len(name) > 80 || !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return PreviewServerConfiguration{}, fmt.Errorf("preview configuration has an invalid name")
	}
	program := strings.TrimSpace(raw.Program)
	if program == "" && len(raw.Args) > 0 {
		return PreviewServerConfiguration{}, fmt.Errorf("preview configuration %q sets args without program", name)
	}
	if program != "" && len(raw.RuntimeArgs) > 0 {
		return PreviewServerConfiguration{}, fmt.Errorf("preview configuration %q sets runtimeArgs with program", name)
	}
	if executable != "" && program != "" {
		return PreviewServerConfiguration{}, fmt.Errorf("preview configuration %q cannot set both runtimeExecutable and program", name)
	}
	if executable == "" && program != "" {
		executable = "node"
		args = append([]string{program}, raw.Args...)
	}
	attachOnly := executable == "" && strings.TrimSpace(raw.URL) != ""
	if !attachOnly && (executable == "" || len(executable) > 1024 || !utf8.ValidString(executable) || strings.ContainsRune(executable, 0)) {
		return PreviewServerConfiguration{}, fmt.Errorf("preview configuration %q has an invalid runtimeExecutable", name)
	}
	if program != "" && (len(program) > 4096 || !utf8.ValidString(program) || strings.ContainsRune(program, 0)) {
		return PreviewServerConfiguration{}, fmt.Errorf("preview configuration %q has an invalid program", name)
	}
	if port == 0 {
		port = 3000
	}
	if port < 1 || port > 65535 {
		return PreviewServerConfiguration{}, fmt.Errorf("preview configuration %q has an invalid port", name)
	}
	if len(args) > previewMaxArgs {
		return PreviewServerConfiguration{}, fmt.Errorf("preview configuration %q has more than %d arguments", name, previewMaxArgs)
	}
	checkedArgs := make([]string, len(args))
	for index, arg := range args {
		if len(arg) > 4096 || !utf8.ValidString(arg) || strings.ContainsRune(arg, 0) {
			return PreviewServerConfiguration{}, fmt.Errorf("preview configuration %q has an invalid argument", name)
		}
		checkedArgs[index] = arg
	}
	cwd, err := validatePreviewCwd(raw.Cwd)
	if err != nil {
		return PreviewServerConfiguration{}, fmt.Errorf("preview configuration %q: %w", name, err)
	}
	env, err := validatePreviewEnv(raw.Env)
	if err != nil {
		return PreviewServerConfiguration{}, fmt.Errorf("preview configuration %q: %w", name, err)
	}
	previewURL, err := validatePreviewURL(raw.URL, port)
	if err != nil {
		return PreviewServerConfiguration{}, fmt.Errorf("preview configuration %q: %w", name, err)
	}
	config := PreviewServerConfiguration{
		Name: name, RuntimeExecutable: executable, RuntimeArgs: checkedArgs, Port: port,
		Cwd: cwd, Env: env, AutoPort: raw.AutoPort, Program: program, Args: append([]string(nil), raw.Args...), URL: previewURL,
	}
	config.Command = displayPreviewReview(config)
	return config, nil
}

func validatePreviewCwd(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == "${workspaceFolder}" {
		return "", nil
	}
	value = strings.TrimPrefix(value, "${workspaceFolder}/")
	if strings.Contains(value, "${") || len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("cwd is invalid")
	}
	normalized := filepath.Clean(filepath.FromSlash(value))
	if filepath.IsAbs(normalized) || normalized == ".." || strings.HasPrefix(normalized, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("cwd must stay inside the session workspace")
	}
	return filepath.ToSlash(normalized), nil
}

func validatePreviewEnv(values map[string]string) (map[string]string, error) {
	if len(values) > 64 {
		return nil, fmt.Errorf("env has more than 64 entries")
	}
	protected := map[string]bool{
		"HOME": true, "PWD": true, "USER": true, "PATH": true, "SHELL": true,
		"TMPDIR": true, "TMP": true, "TEMP": true,
		"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true,
		"GOCACHE": true, "GOPATH": true, "GOROOT": true, "NPM_CONFIG_CACHE": true, "PIP_CACHE_DIR": true,
		"HOST": true, "PORT": true, "BROWSER": true,
	}
	result := make(map[string]string, len(values))
	seen := make(map[string]bool, len(values))
	for key, value := range values {
		upper := strings.ToUpper(key)
		validKey := key != "" && len(key) <= 256
		for index, character := range key {
			if !((character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || character == '_' || (index > 0 && character >= '0' && character <= '9')) {
				validKey = false
				break
			}
		}
		if !validKey || seen[upper] || len(value) > 16384 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("env contains an invalid key or value")
		}
		if protected[upper] {
			return nil, fmt.Errorf("env cannot override isolated variable %s", key)
		}
		seen[upper] = true
		result[key] = value
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

func validatePreviewURL(value string, port int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", fmt.Errorf("url must be an HTTP(S) address without credentials or fragment")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	ip := net.ParseIP(host)
	if host != "localhost" && !strings.HasSuffix(host, ".localhost") && (ip == nil || !ip.IsLoopback()) {
		return "", fmt.Errorf("external url requires browser domain approval, which is not available in this preview pane")
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" {
		return "", fmt.Errorf("localhost url must contain only the server origin")
	}
	urlPort, err := strconv.Atoi(parsed.Port())
	if err != nil || urlPort != port {
		return "", fmt.Errorf("url port must match port %d", port)
	}
	return strings.TrimSuffix(value, "/"), nil
}

func displayPreviewReview(config PreviewServerConfiguration) string {
	command := "attach to existing server"
	if config.RuntimeExecutable != "" {
		command = displayPreviewCommand(config.RuntimeExecutable, config.RuntimeArgs)
	}
	lines := []string{"command: " + command, "cwd: " + firstNonEmpty(config.Cwd, "."), "port: " + strconv.Itoa(config.Port)}
	if config.URL != "" {
		lines = append(lines, "url: "+config.URL)
	}
	if config.AutoPort != nil {
		lines = append(lines, "autoPort: "+strconv.FormatBool(*config.AutoPort))
	}
	if len(config.Env) > 0 {
		keys := make([]string, 0, len(config.Env))
		for key := range config.Env {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			lines = append(lines, "env "+key+"="+strconv.Quote(config.Env[key]))
		}
	}
	return strings.Join(lines, "\n")
}

func displayPreviewCommand(executable string, args []string) string {
	parts := []string{strconv.Quote(executable)}
	for _, arg := range args {
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}

func newPreviewBridgeToken() (string, error) {
	data := make([]byte, 24)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	for index := range data {
		data[index] = alphabet[int(data[index])%len(alphabet)]
	}
	return string(data), nil
}

func previewBridgeScript(token string, bootstrap previewStorageBootstrap) string {
	encoded, _ := json.Marshal(token)
	encodedBootstrap, _ := json.Marshal(bootstrap)
	/* legacy bridge body retained temporarily for review; superseded below.
	return `(function(){"use strict";const token=` + string(encoded) + `,issues=[];const push=(kind,value)=>{const text=String(value&&value.message||value||"").slice(0,2000);if(text)issues.push({kind,text,time:Date.now()});if(issues.length>100)issues.shift()};window.addEventListener("error",e=>push(e.target&&e.target!==window?"resource":"error",e.message||(e.target&&e.target.src)||"resource failed"),true);window.addEventListener("unhandledrejection",e=>push("unhandledrejection",e.reason));for(const level of ["error","warn"]){const original=console[level];console[level]=function(){push("console."+level,[...arguments].map(String).join(" "));return original.apply(this,arguments)}}const visible=e=>{const r=e.getBoundingClientRect(),s=getComputedStyle(e);return r.width>0&&r.height>0&&s.visibility!=="hidden"&&s.display!=="none"},describe=e=>({tag:e.tagName.toLowerCase(),role:e.getAttribute("role")||"",type:e.getAttribute("type")||"",name:e.getAttribute("name")||"",id:e.id||"",text:(e.innerText||e.getAttribute("aria-label")||e.getAttribute("alt")||"").trim().replace(/\s+/g," ").slice(0,500),disabled:!!e.disabled,rect:(()=>{const r=e.getBoundingClientRect();return{x:Math.round(r.x),y:Math.round(r.y),width:Math.round(r.width),height:Math.round(r.height)}})()}),sameOrigin=e=>{const link=e.closest&&e.closest("a[href]");if(link&&new URL(link.href,location.href).origin!==location.origin)return false;const form=e.closest&&e.closest("form[action]");return !form||new URL(form.action,location.href).origin===location.origin},shot=async()=>{try{const w=Math.min(innerWidth,1600),h=Math.min(innerHeight,1200),clone=document.documentElement.cloneNode(true),svg=`<svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}"><foreignObject width="100%" height="100%">${new XMLSerializer().serializeToString(clone)}</foreignObject></svg>`,img=new Image;await new Promise((resolve,reject)=>{img.onload=resolve;img.onerror=reject;img.src="data:image/svg+xml;charset=utf-8,"+encodeURIComponent(svg)});const canvas=document.createElement("canvas");canvas.width=w;canvas.height=h;canvas.getContext("2d").drawImage(img,0,0);const data=canvas.toDataURL("image/png");return data.length<=4*1024*1024?data:""}catch(error){push("screenshot",error);return""}},snapshot=async(withShot)=>{let controls=[],headings=[];try{controls=[...document.querySelectorAll("a[href],button,input,select,textarea,[role=button],[tabindex]")].filter(visible).slice(0,300).map(describe);headings=[...document.querySelectorAll("h1,h2,h3,h4,h5,h6,[role=heading]")].filter(visible).slice(0,100).map(describe)}catch(error){push("bridge",error)}return{url:location.href,title:document.title,readyState:document.readyState,viewport:{width:innerWidth,height:innerHeight,devicePixelRatio},text:(document.body&&document.body.innerText||"").trim().replace(/\n{3,}/g,"\n\n").slice(0,50000),headings,controls,issues:issues.slice(),capturedAt:Date.now(),screenshotDataURL:withShot?await shot():""}};window.addEventListener("message",async e=>{const d=e.data;if(e.source!==parent||!d||d.type!=="gokin-preview-command"||d.token!==token)return;let actionResult="inspected";try{const a=d.args||{},action=a.action||"inspect";if(action==="click"||action==="fill"){const el=document.elementFromPoint(Number(a.x),Number(a.y));if(!el||!sameOrigin(el))throw new Error("target is missing or would leave the localhost preview");if(action==="click")el.click();else{const input=el.closest("input,textarea,[contenteditable=true]");if(!input)throw new Error("fill target is not editable");if(input.isContentEditable)input.textContent=String(a.text||"");else{const setter=Object.getOwnPropertyDescriptor(Object.getPrototypeOf(input),"value")?.set;setter?setter.call(input,String(a.text||"")):input.value=String(a.text||"")}input.dispatchEvent(new Event("input",{bubbles:true}));input.dispatchEvent(new Event("change",{bubbles:true}));input.focus()}actionResult=action+" completed"}else if(action==="scroll"){scrollBy({top:Number(a.deltaY)||0,behavior:"instant"});actionResult="scroll completed"}else if(action==="key"){const key=String(a.key||"");(document.activeElement||document.body).dispatchEvent(new KeyboardEvent("keydown",{key:key==="SPACE"?" ":key,bubbles:true}));actionResult="key dispatched"}await new Promise(r=>setTimeout(r,action==="inspect"?0:250));const payload=await snapshot(a.screenshot!==false);payload.actionResult=actionResult;parent.postMessage({type:"gokin-preview-result",token,requestId:d.requestId,payload},"*")}catch(error){parent.postMessage({type:"gokin-preview-result",token,requestId:d.requestId,payload:{error:String(error&&error.message||error),issues:issues.slice(),capturedAt:Date.now()}},"*")}});parent.postMessage({type:"gokin-preview-ready",token},"*")})();`
	*/
	return previewBridgeScriptV3(string(encoded), string(encodedBootstrap))
}

func previewBridgeScriptV2(encodedToken, encodedBootstrap string) string {
	return `(function(){"use strict";const token=` + encodedToken + `,persistence=` + encodedBootstrap + `,issues=[];const captureStorage=()=>{const local={};try{for(let i=0;i<localStorage.length&&i<512;i++){const key=localStorage.key(i);if(key!==null)local[key]=String(localStorage.getItem(key)||"").slice(0,131072)}}catch(_){}return{localStorage:local,cookies:String(document.cookie||"").slice(0,65536)}};const postStorage=()=>{if(persistence.enabled)parent.postMessage({type:"gokin-preview-storage",token,payload:captureStorage()},"*")};let storageTimer=0;const scheduleStorage=()=>{if(!persistence.enabled)return;clearTimeout(storageTimer);storageTimer=setTimeout(postStorage,80)};if(persistence.enabled){try{if(localStorage.length===0)for(const [key,value] of Object.entries(persistence.localStorage||{}))localStorage.setItem(key,String(value))}catch(_){}for(const method of ["setItem","removeItem","clear"]){const original=Storage.prototype[method];Storage.prototype[method]=function(){const result=original.apply(this,arguments);if(this===localStorage)scheduleStorage();return result}}window.addEventListener("pagehide",postStorage);document.addEventListener("visibilitychange",()=>{if(document.visibilityState==="hidden")postStorage()})}const push=(kind,value)=>{const text=String(value&&value.message||value||"").slice(0,2000);if(text)issues.push({kind,text,time:Date.now()});if(issues.length>100)issues.shift()};window.addEventListener("error",e=>push(e.target&&e.target!==window?"resource":"error",e.message||(e.target&&e.target.src)||"resource failed"),true);window.addEventListener("unhandledrejection",e=>push("unhandledrejection",e.reason));for(const level of ["error","warn"]){const original=console[level];console[level]=function(){push("console."+level,[...arguments].map(String).join(" "));return original.apply(this,arguments)}}const visible=e=>{const r=e.getBoundingClientRect(),s=getComputedStyle(e);return r.width>0&&r.height>0&&s.visibility!=="hidden"&&s.display!=="none"},describe=e=>({tag:e.tagName.toLowerCase(),role:e.getAttribute("role")||"",type:e.getAttribute("type")||"",name:e.getAttribute("name")||"",id:e.id||"",text:(e.innerText||e.getAttribute("aria-label")||e.getAttribute("alt")||"").trim().replace(/\s+/g," ").slice(0,500),disabled:!!e.disabled,rect:(()=>{const r=e.getBoundingClientRect();return{x:Math.round(r.x),y:Math.round(r.y),width:Math.round(r.width),height:Math.round(r.height)}})()}),sameOrigin=e=>{const link=e.closest&&e.closest("a[href]");if(link&&new URL(link.href,location.href).origin!==location.origin)return false;const form=e.closest&&e.closest("form[action]");return !form||new URL(form.action,location.href).origin===location.origin},shot=async()=>{try{const w=Math.min(innerWidth,1600),h=Math.min(innerHeight,1200),clone=document.documentElement.cloneNode(true),svg='<svg xmlns="http://www.w3.org/2000/svg" width="'+w+'" height="'+h+'"><foreignObject width="100%" height="100%">'+new XMLSerializer().serializeToString(clone)+'</foreignObject></svg>',img=new Image;await new Promise((resolve,reject)=>{img.onload=resolve;img.onerror=reject;img.src="data:image/svg+xml;charset=utf-8,"+encodeURIComponent(svg)});const canvas=document.createElement("canvas");canvas.width=w;canvas.height=h;canvas.getContext("2d").drawImage(img,0,0);const data=canvas.toDataURL("image/png");return data.length<=4*1024*1024?data:""}catch(error){push("screenshot",error);return""}},snapshot=async withShot=>{let controls=[],headings=[];try{controls=[...document.querySelectorAll("a[href],button,input,select,textarea,[role=button],[tabindex]")].filter(visible).slice(0,300).map(describe);headings=[...document.querySelectorAll("h1,h2,h3,h4,h5,h6,[role=heading]")].filter(visible).slice(0,100).map(describe)}catch(error){push("bridge",error)}return{url:location.href,title:document.title,readyState:document.readyState,viewport:{width:innerWidth,height:innerHeight,devicePixelRatio},text:(document.body&&document.body.innerText||"").trim().replace(/\n{3,}/g,"\n\n").slice(0,50000),headings,controls,issues:issues.slice(),capturedAt:Date.now(),screenshotDataURL:withShot?await shot():""}};window.addEventListener("message",async e=>{const d=e.data;if(e.source!==parent||!d||d.token!==token)return;if(d.type==="gokin-preview-storage-clear"){try{localStorage.clear();for(const part of String(document.cookie||"").split(";")){const name=part.split("=")[0].trim();if(name)document.cookie=name+"=; Max-Age=0; Path=/"}}catch(_){}parent.postMessage({type:"gokin-preview-storage-cleared",token},"*");return}if(d.type!=="gokin-preview-command")return;try{const a=d.args||{},action=a.action||"inspect";let actionResult="inspected";if(action==="click"||action==="fill"){const el=document.elementFromPoint(Number(a.x),Number(a.y));if(!el||!sameOrigin(el))throw new Error("target is missing or would leave the localhost preview");if(action==="click")el.click();else{const input=el.closest("input,textarea,[contenteditable=true]");if(!input)throw new Error("fill target is not editable");if(input.isContentEditable)input.textContent=String(a.text||"");else{const setter=Object.getOwnPropertyDescriptor(Object.getPrototypeOf(input),"value")?.set;setter?setter.call(input,String(a.text||"")):input.value=String(a.text||"")}input.dispatchEvent(new Event("input",{bubbles:true}));input.dispatchEvent(new Event("change",{bubbles:true}));input.focus()}actionResult=action+" completed"}else if(action==="scroll"){scrollBy({top:Number(a.deltaY)||0,behavior:"instant"});actionResult="scroll completed"}else if(action==="key"){const key=String(a.key||"");(document.activeElement||document.body).dispatchEvent(new KeyboardEvent("keydown",{key:key==="SPACE"?" ":key,bubbles:true}));actionResult="key dispatched"}await new Promise(r=>setTimeout(r,action==="inspect"?0:250));postStorage();const payload=await snapshot(a.screenshot!==false);payload.actionResult=actionResult;parent.postMessage({type:"gokin-preview-result",token,requestId:d.requestId,payload},"*")}catch(error){parent.postMessage({type:"gokin-preview-result",token,requestId:d.requestId,payload:{error:String(error&&error.message||error),issues:issues.slice(),capturedAt:Date.now()}},"*")}},false);parent.postMessage({type:"gokin-preview-ready",token},"*");setTimeout(postStorage,0)})();`
}

// previewBridgeScriptV3 keeps the diagnostics/storage behavior of V2 and adds
// an explicit user-driven element picker. Selection stays inside the proxied
// preview document: capture-phase pointer handlers suppress the page's own
// click, a pointer-events:none overlay highlights the target, and only bounded
// descriptive metadata is returned through the token-authenticated parent
// channel. It never exposes outer-document DOM or executes a selected element.
func previewBridgeScriptV3(encodedToken, encodedBootstrap string) string {
	return `(function(){
"use strict";
const token=` + encodedToken + `,persistence=` + encodedBootstrap + `,issues=[];
const captureStorage=()=>{const local={};try{for(let i=0;i<localStorage.length&&i<512;i++){const key=localStorage.key(i);if(key!==null)local[key]=String(localStorage.getItem(key)||"").slice(0,131072)}}catch(_){}return{localStorage:local,cookies:String(document.cookie||"").slice(0,65536)}};
const postStorage=()=>{if(persistence.enabled)parent.postMessage({type:"gokin-preview-storage",token,payload:captureStorage()},"*")};
let storageTimer=0;
const scheduleStorage=()=>{if(!persistence.enabled)return;clearTimeout(storageTimer);storageTimer=setTimeout(postStorage,80)};
if(persistence.enabled){
  try{if(localStorage.length===0)for(const [key,value] of Object.entries(persistence.localStorage||{}))localStorage.setItem(key,String(value))}catch(_){}
  for(const method of ["setItem","removeItem","clear"]){const original=Storage.prototype[method];Storage.prototype[method]=function(){const result=original.apply(this,arguments);if(this===localStorage)scheduleStorage();return result}}
  window.addEventListener("pagehide",postStorage);
  document.addEventListener("visibilitychange",()=>{if(document.visibilityState==="hidden")postStorage()});
}
const push=(kind,value)=>{const text=String(value&&value.message||value||"").slice(0,2000);if(text)issues.push({kind,text,time:Date.now()});if(issues.length>100)issues.shift()};
window.addEventListener("error",e=>push(e.target&&e.target!==window?"resource":"error",e.message||(e.target&&e.target.src)||"resource failed"),true);
window.addEventListener("unhandledrejection",e=>push("unhandledrejection",e.reason));
for(const level of ["error","warn"]){const original=console[level];console[level]=function(){push("console."+level,[...arguments].map(String).join(" "));return original.apply(this,arguments)}}
const visible=e=>{const r=e.getBoundingClientRect(),s=getComputedStyle(e);return r.width>0&&r.height>0&&s.visibility!=="hidden"&&s.display!=="none"};
const describe=e=>({
  tag:String(e.tagName||"").toLowerCase().slice(0,80),
  role:String(e.getAttribute("role")||"").slice(0,120),
  type:String(e.getAttribute("type")||"").slice(0,120),
  name:String(e.getAttribute("name")||"").slice(0,200),
  id:String(e.id||"").slice(0,200),
  testId:String(e.getAttribute("data-testid")||e.getAttribute("data-test")||"").slice(0,200),
  text:String(e.innerText||e.getAttribute("aria-label")||e.getAttribute("alt")||"").trim().replace(/\s+/g," ").slice(0,500),
  disabled:!!e.disabled,
  rect:(()=>{const r=e.getBoundingClientRect();return{x:Math.round(r.x),y:Math.round(r.y),width:Math.round(r.width),height:Math.round(r.height)}})()
});
const cssEscape=value=>window.CSS&&typeof CSS.escape==="function"?CSS.escape(String(value)):String(value).replace(/[^a-zA-Z0-9_-]/g,"\\$&");
const selectorFor=e=>{
  if(e.id)return("#"+cssEscape(e.id)).slice(0,512);
  const parts=[];let node=e;
  for(let depth=0;node&&node.nodeType===1&&depth<6;depth++,node=node.parentElement){
    let part=String(node.tagName||"*").toLowerCase();
    for(const attr of ["data-testid","data-test","name"]){const value=node.getAttribute(attr);if(value){part+="["+attr+"="+JSON.stringify(String(value).slice(0,120))+"]";break}}
    if(!part.includes("[")){const classes=[...node.classList].filter(value=>value&&value.length<=64).slice(0,2);if(classes.length)part+="."+classes.map(cssEscape).join(".")}
    const parentNode=node.parentElement;
    if(parentNode){const siblings=[...parentNode.children].filter(item=>item.tagName===node.tagName);if(siblings.length>1)part+=":nth-of-type("+(siblings.indexOf(node)+1)+")"}
    parts.unshift(part);
    const candidate=parts.join(" > ");
    try{if(document.querySelectorAll(candidate).length===1)return candidate.slice(0,512)}catch(_){}
  }
  return parts.join(" > ").slice(0,512);
};
const sameOrigin=e=>{const link=e.closest&&e.closest("a[href]");if(link&&new URL(link.href,location.href).origin!==location.origin)return false;const form=e.closest&&e.closest("form[action]");return!form||new URL(form.action,location.href).origin===location.origin};
const shot=async()=>{try{const w=Math.min(innerWidth,1600),h=Math.min(innerHeight,1200),clone=document.documentElement.cloneNode(true),svg='<svg xmlns="http://www.w3.org/2000/svg" width="'+w+'" height="'+h+'"><foreignObject width="100%" height="100%">'+new XMLSerializer().serializeToString(clone)+'</foreignObject></svg>',img=new Image;await new Promise((resolve,reject)=>{img.onload=resolve;img.onerror=reject;img.src="data:image/svg+xml;charset=utf-8,"+encodeURIComponent(svg)});const canvas=document.createElement("canvas");canvas.width=w;canvas.height=h;canvas.getContext("2d").drawImage(img,0,0);const data=canvas.toDataURL("image/png");return data.length<=4*1024*1024?data:""}catch(error){push("screenshot",error);return""}};
const snapshot=async withShot=>{let controls=[],headings=[];try{controls=[...document.querySelectorAll("a[href],button,input,select,textarea,[role=button],[tabindex]")].filter(visible).slice(0,300).map(describe);headings=[...document.querySelectorAll("h1,h2,h3,h4,h5,h6,[role=heading]")].filter(visible).slice(0,100).map(describe)}catch(error){push("bridge",error)}return{url:location.href,title:document.title,readyState:document.readyState,viewport:{width:innerWidth,height:innerHeight,devicePixelRatio},text:String(document.body&&document.body.innerText||"").trim().replace(/\n{3,}/g,"\n\n").slice(0,50000),headings,controls,issues:issues.slice(),capturedAt:Date.now(),screenshotDataURL:withShot?await shot():""}};
let selection=null;
const removeSelectionListeners=()=>{document.removeEventListener("pointermove",selectionMove,true);document.removeEventListener("pointerdown",selectionBlock,true);document.removeEventListener("mousedown",selectionBlock,true);document.removeEventListener("click",selectionClick,true);window.removeEventListener("keydown",selectionKey,true)};
const finishSelection=(payload,notify)=>{const current=selection;if(!current)return;selection=null;removeSelectionListeners();current.overlay.remove();current.style.remove();if(notify)parent.postMessage({type:"gokin-preview-result",token,requestId:current.requestId,payload},"*")};
const selectionTarget=event=>event.target instanceof Element&&event.target.id!=="__gokin_preview_selector"?event.target:null;
const selectionMove=event=>{if(!selection)return;const target=selectionTarget(event);if(!target)return;const rect=target.getBoundingClientRect(),rule=selection.rule.style;rule.left=Math.max(0,rect.left)+"px";rule.top=Math.max(0,rect.top)+"px";rule.width=Math.max(0,Math.min(innerWidth-rect.left,rect.width))+"px";rule.height=Math.max(0,Math.min(innerHeight-rect.top,rect.height))+"px"};
const selectionBlock=event=>{if(!selection)return;event.preventDefault();event.stopPropagation();event.stopImmediatePropagation()};
const selectionClick=event=>{if(!selection)return;const target=selectionTarget(event);event.preventDefault();event.stopPropagation();event.stopImmediatePropagation();if(!target)return;const ancestors=[];for(let node=target.parentElement;node&&ancestors.length<4;node=node.parentElement)ancestors.push(describe(node));finishSelection({actionResult:"element selected",selectedElement:{selector:selectorFor(target),element:describe(target),ancestors,url:String(location.href).slice(0,2048),title:String(document.title).slice(0,500),capturedAt:Date.now()}},true)};
const selectionKey=event=>{if(!selection||event.key!=="Escape")return;event.preventDefault();event.stopPropagation();event.stopImmediatePropagation();finishSelection({actionResult:"element selection cancelled",cancelled:true,capturedAt:Date.now()},true)};
const selectionShortcut=event=>{if(selection||event.altKey||!(event.metaKey||event.ctrlKey)||!event.shiftKey||!(event.code==="KeyS"||String(event.key).toLowerCase()==="s"))return;event.preventDefault();event.stopPropagation();event.stopImmediatePropagation();parent.postMessage({type:"gokin-preview-select-request",token},"*")};
const beginSelection=requestId=>{if(selection)finishSelection({actionResult:"element selection replaced",cancelled:true,capturedAt:Date.now()},true);const style=document.createElement("style");style.nonce=token;style.textContent="#__gokin_preview_selector{position:fixed;z-index:2147483647;pointer-events:none;box-sizing:border-box;border:2px solid #8b5cf6;background:rgba(139,92,246,.12);box-shadow:0 0 0 1px rgba(255,255,255,.75) inset;border-radius:3px;left:0;top:0;width:0;height:0}html{cursor:crosshair!important}";document.head.appendChild(style);const overlay=document.createElement("div");overlay.id="__gokin_preview_selector";overlay.setAttribute("aria-hidden","true");document.documentElement.appendChild(overlay);const rule=style.sheet&&style.sheet.cssRules&&style.sheet.cssRules[0];if(!rule){overlay.remove();style.remove();parent.postMessage({type:"gokin-preview-result",token,requestId,payload:{error:"preview CSP blocked the element-selection overlay",capturedAt:Date.now()}},"*");return}selection={requestId,overlay,style,rule};document.addEventListener("pointermove",selectionMove,true);document.addEventListener("pointerdown",selectionBlock,true);document.addEventListener("mousedown",selectionBlock,true);document.addEventListener("click",selectionClick,true);window.addEventListener("keydown",selectionKey,true)};
window.addEventListener("message",async e=>{
  const d=e.data;if(e.source!==parent||!d||d.token!==token)return;
  if(d.type==="gokin-preview-storage-clear"){try{localStorage.clear();for(const part of String(document.cookie||"").split(";")){const name=part.split("=")[0].trim();if(name)document.cookie=name+"=; Max-Age=0; Path=/"}}catch(_){}parent.postMessage({type:"gokin-preview-storage-cleared",token},"*");return}
  if(d.type!=="gokin-preview-command"||typeof d.requestId!=="string")return;
  const a=d.args||{},action=a.action||"inspect";
  if(action==="select_element"){beginSelection(d.requestId);return}
  if(action==="cancel_element_selection"){finishSelection({actionResult:"element selection cancelled",cancelled:true,capturedAt:Date.now()},true);parent.postMessage({type:"gokin-preview-result",token,requestId:d.requestId,payload:{actionResult:"selection cancel requested",cancelled:true,capturedAt:Date.now()}},"*");return}
  try{let actionResult="inspected";if(action==="click"||action==="fill"){const el=document.elementFromPoint(Number(a.x),Number(a.y));if(!el||!sameOrigin(el))throw new Error("target is missing or would leave the localhost preview");if(action==="click")el.click();else{const input=el.closest("input,textarea,[contenteditable=true]");if(!input)throw new Error("fill target is not editable");if(input.isContentEditable)input.textContent=String(a.text||"");else{const setter=Object.getOwnPropertyDescriptor(Object.getPrototypeOf(input),"value")?.set;setter?setter.call(input,String(a.text||"")):input.value=String(a.text||"")}input.dispatchEvent(new Event("input",{bubbles:true}));input.dispatchEvent(new Event("change",{bubbles:true}));input.focus()}actionResult=action+" completed"}else if(action==="scroll"){scrollBy({top:Number(a.deltaY)||0,behavior:"instant"});actionResult="scroll completed"}else if(action==="key"){const key=String(a.key||"");(document.activeElement||document.body).dispatchEvent(new KeyboardEvent("keydown",{key:key==="SPACE"?" ":key,bubbles:true}));actionResult="key dispatched"}await new Promise(resolve=>setTimeout(resolve,action==="inspect"?0:250));postStorage();const payload=await snapshot(a.screenshot!==false);payload.actionResult=actionResult;parent.postMessage({type:"gokin-preview-result",token,requestId:d.requestId,payload},"*")}catch(error){parent.postMessage({type:"gokin-preview-result",token,requestId:d.requestId,payload:{error:String(error&&error.message||error),issues:issues.slice(),capturedAt:Date.now()}},"*")}
},false);
window.addEventListener("pagehide",()=>finishSelection({actionResult:"preview navigated",cancelled:true,capturedAt:Date.now()},true));
window.addEventListener("keydown",selectionShortcut,true);
parent.postMessage({type:"gokin-preview-ready",token},"*");setTimeout(postStorage,0);
})();`
}

func addPreviewBridgeNonce(policy, token string) string {
	nonce := "'nonce-" + token + "'"
	parts := strings.Split(policy, ";")
	seen := make(map[string]bool)
	for index, part := range parts {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		name := strings.ToLower(fields[0])
		switch name {
		case "script-src", "script-src-elem", "style-src", "style-src-elem":
			seen[name] = true
			if !strings.Contains(part, nonce) {
				parts[index] = strings.TrimSpace(part) + " " + nonce
			}
		}
	}
	if !seen["script-src"] && !seen["script-src-elem"] {
		parts = append(parts, " script-src "+nonce)
	}
	if !seen["style-src"] && !seen["style-src-elem"] {
		parts = append(parts, " style-src "+nonce)
	}
	return strings.Join(parts, ";")
}

func startPreviewBridgeProxy(run *previewServerRun) error {
	target, err := url.Parse(run.targetURL)
	if err != nil {
		return fmt.Errorf("parse preview target: %w", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		incomingCookies := request.Cookies()
		originalDirector(request)
		request.Host = target.Host
		request.Header.Del("Accept-Encoding")
		request.Header.Del("Cookie")
		if run.staticAccessToken != "" {
			request.Header.Set("X-Gokin-Static-Token", run.staticAccessToken)
		}
		if run.profile != nil {
			_ = run.profile.mergeCookies(incomingCookies)
			for _, cookie := range run.profile.requestCookies(request.URL.Path) {
				request.AddCookie(cookie)
			}
		}
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		if run.profile != nil {
			upstreamCookies := response.Cookies()
			if err := run.profile.mergeCookies(upstreamCookies); err != nil {
				return fmt.Errorf("persist preview cookies: %w", err)
			}
			response.Header.Del("Set-Cookie")
			for _, cookie := range upstreamCookies {
				if cookie.MaxAge < 0 {
					cookie.Domain = ""
					if cookie.Path == "" {
						cookie.Path = "/"
					}
					response.Header.Add("Set-Cookie", cookie.String())
				}
			}
			for _, cookie := range run.profile.responseCookies() {
				cookie.Domain = ""
				response.Header.Add("Set-Cookie", cookie.String())
			}
		}
		contentType := strings.ToLower(response.Header.Get("Content-Type"))
		if response.StatusCode < 200 || response.StatusCode >= 300 || !strings.Contains(contentType, "text/html") || response.Header.Get("Content-Encoding") != "" {
			return nil
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20+1))
		_ = response.Body.Close()
		if err != nil {
			return err
		}
		if len(body) > 8<<20 {
			return fmt.Errorf("preview HTML exceeds the 8 MiB bridge limit")
		}
		bootstrap := previewStorageBootstrap{}
		if run.profile != nil {
			bootstrap = run.profile.bootstrap()
		}
		script := `<script nonce="` + run.bridgeToken + `">` + previewBridgeScript(run.bridgeToken, bootstrap) + `</script>`
		lower := strings.ToLower(string(body))
		position := -1
		if head := strings.Index(lower, "<head"); head >= 0 {
			if close := strings.Index(lower[head:], ">"); close >= 0 {
				position = head + close + 1
			}
		}
		if position < 0 {
			position = strings.Index(lower, "<body")
		}
		if position < 0 {
			position = 0
		}
		body = append(append(append([]byte(nil), body[:position]...), []byte(script)...), body[position:]...)
		response.Body = io.NopCloser(bytes.NewReader(body))
		response.ContentLength = int64(len(body))
		response.Header.Set("Content-Length", strconv.Itoa(len(body)))
		if policy := response.Header.Get("Content-Security-Policy"); policy != "" {
			response.Header.Set("Content-Security-Policy", addPreviewBridgeNonce(policy, run.bridgeToken))
		}
		return nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	handler := http.Handler(proxy)
	if run.staticAccessToken != "" {
		handler = staticPreviewAccessHandler(run.staticAccessToken, proxy)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	run.proxy = server
	run.proxyURL = "http://" + listener.Addr().String()
	port := listener.Addr().(*net.TCPAddr).Port
	// Must be a loopback IP literal, not a "<token>.localhost" name. macOS App
	// Transport Security exempts only bare `localhost` and loopback literals;
	// it treats `<label>.localhost` as an ordinary qualified public domain and
	// refuses the insecure load with NSURLErrorDomain -1022, so the pane never
	// rendered in the WebView. (CFNetwork does not resolve those names either,
	// so an ATS exception alone would not help.) The per-run ephemeral port
	// still gives every preview its own web origin, and the bridge remains
	// bound by bridgeToken plus the staticAccessToken gate.
	run.browserURL = "http://127.0.0.1:" + strconv.Itoa(port)
	go func() { _ = server.Serve(listener) }()
	return nil
}

func detectPreviewConfiguration(dir string) *PreviewServerConfiguration {
	data, _, err := readProjectRegularFile(dir, "package.json", 2<<20)
	if err != nil {
		return nil
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return nil
	}
	script := ""
	if pkg.Scripts["dev"] != "" {
		script = "dev"
	} else if pkg.Scripts["start"] != "" {
		script = "start"
	}
	if script == "" {
		return nil
	}
	config, err := validatePreviewConfiguration("Detected "+script, "npm", []string{"run", script}, 3000)
	if err != nil {
		return nil
	}
	return &config
}

func stripJSONComments(data []byte) ([]byte, error) {
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("configuration is not valid UTF-8")
	}
	out := append([]byte(nil), data...)
	inString := false
	escaped := false
	for index := 0; index < len(out); index++ {
		if inString {
			if escaped {
				escaped = false
			} else if out[index] == '\\' {
				escaped = true
			} else if out[index] == '"' {
				inString = false
			}
			continue
		}
		if out[index] == '"' {
			inString = true
			continue
		}
		if out[index] == '/' && index+1 < len(out) && out[index+1] == '/' {
			out[index], out[index+1] = ' ', ' '
			index += 2
			for index < len(out) && out[index] != '\n' && out[index] != '\r' {
				out[index] = ' '
				index++
			}
			index--
			continue
		}
		if out[index] == '/' && index+1 < len(out) && out[index+1] == '*' {
			out[index], out[index+1] = ' ', ' '
			index += 2
			closed := false
			for index < len(out)-1 {
				if out[index] == '*' && out[index+1] == '/' {
					out[index], out[index+1] = ' ', ' '
					index++
					closed = true
					break
				}
				if out[index] != '\n' && out[index] != '\r' {
					out[index] = ' '
				}
				index++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated block comment")
			}
		}
	}
	return out, nil
}

func (s *Studio) StartSessionPreviewServer(projectID, sessionID, configuration, reviewedCommand string) (*PreviewServerStatus, error) {
	configSet, err := s.GetSessionPreviewConfig(projectID, sessionID)
	if err != nil {
		return nil, err
	}
	var selected *PreviewServerConfiguration
	for index := range configSet.Configurations {
		if configSet.Configurations[index].Name == configuration {
			selected = &configSet.Configurations[index]
			break
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("preview configuration not found: %s", configuration)
	}
	if reviewedCommand != selected.Command {
		return nil, fmt.Errorf("preview configuration changed after review; review the current command before starting")
	}
	p, session, err := s.projectSession(projectID, sessionID)
	if err != nil {
		return nil, err
	}
	dir, err := sessionWorkingDirectory(p, session)
	if err != nil {
		return nil, err
	}
	key := previewServerKey(projectID, sessionID, selected.Name)
	s.previewMu.Lock()
	if s.previewServers == nil {
		s.previewServers = make(map[string]*previewServerRun)
	}
	if existing := s.previewServers[key]; existing != nil {
		existing.mu.RLock()
		active := existing.state == "starting" || existing.state == "running"
		existing.mu.RUnlock()
		if active {
			s.previewMu.Unlock()
			return previewStatus(existing), nil
		}
	}
	selectedRun := *selected
	selected = &selectedRun
	preferredPort := selected.Port
	attachOnly := selected.RuntimeExecutable == ""
	if !attachOnly {
		listener, listenErr := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(selected.Port)))
		if listenErr != nil {
			if selected.AutoPort == nil || !*selected.AutoPort {
				s.previewMu.Unlock()
				return nil, fmt.Errorf("preview port %d is already in use; set autoPort to true to choose a free port", selected.Port)
			}
			listener, listenErr = net.Listen("tcp", "127.0.0.1:0")
			if listenErr != nil {
				s.previewMu.Unlock()
				return nil, fmt.Errorf("choose automatic preview port: %w", listenErr)
			}
			selected.Port = listener.Addr().(*net.TCPAddr).Port
		}
		_ = listener.Close()
	}
	workingDir, err := resolvePreviewWorkingDirectory(dir, selected.Cwd)
	if err != nil {
		s.previewMu.Unlock()
		return nil, err
	}
	env, err := security.WorkspaceSafeEnvironment(dir)
	if err != nil {
		s.previewMu.Unlock()
		return nil, err
	}
	baseContext := s.ctx
	if baseContext == nil {
		baseContext = context.Background()
	}
	ctx, cancel := context.WithCancel(baseContext)
	targetURL := selected.URL
	if targetURL == "" || selected.Port != preferredPort {
		targetURL = "http://127.0.0.1:" + strconv.Itoa(selected.Port)
	}
	bridgeToken, err := newPreviewBridgeToken()
	if err != nil {
		cancel()
		s.previewMu.Unlock()
		return nil, fmt.Errorf("create preview bridge token: %w", err)
	}
	profile, err := loadPreviewSessionProfile(projectID, sessionID, selected.Name)
	if err != nil {
		cancel()
		s.previewMu.Unlock()
		return nil, err
	}
	run := &previewServerRun{projectID: projectID, sessionID: sessionID, config: *selected, cancel: cancel, state: "starting", startedAt: time.Now().UnixMilli(), bridgeToken: bridgeToken, autoVerify: configSet.AutoVerify, targetURL: targetURL, profile: profile}
	if attachOnly {
		if err := probePreviewTarget(targetURL, 2*time.Second); err != nil {
			cancel()
			s.previewMu.Unlock()
			return nil, err
		}
		if err := startPreviewBridgeProxy(run); err != nil {
			cancel()
			s.previewMu.Unlock()
			return nil, fmt.Errorf("start preview diagnostics bridge: %w", err)
		}
		run.state = "running"
		s.previewServers[key] = run
		s.previewMu.Unlock()
		return previewStatus(run), nil
	}
	executable, err := resolvePreviewExecutable(workingDir, selected.RuntimeExecutable)
	if err != nil {
		cancel()
		s.previewMu.Unlock()
		return nil, err
	}
	cmd := exec.CommandContext(ctx, executable, selected.RuntimeArgs...)
	cmd.Dir = workingDir
	cmd.Env = mergePreviewEnvironment(env, selected.Env,
		"PWD="+workingDir,
		"HOST=127.0.0.1",
		"PORT="+strconv.Itoa(selected.Port),
		"BROWSER=none",
	)
	preparePreviewProcess(cmd)
	run.cmd = cmd
	cmd.Stdout = &run.logs
	cmd.Stderr = &run.logs
	if err := cmd.Start(); err != nil {
		cancel()
		s.previewMu.Unlock()
		return nil, fmt.Errorf("start preview server: %w", err)
	}
	if err := startPreviewBridgeProxy(run); err != nil {
		cancel()
		_ = killPreviewProcess(cmd)
		_ = cmd.Wait()
		s.previewMu.Unlock()
		return nil, fmt.Errorf("start preview diagnostics bridge: %w", err)
	}
	s.previewServers[key] = run
	s.previewMu.Unlock()
	if !s.startBackground("preview-server", func() { s.monitorPreviewServer(run) }) {
		cancel()
		_ = killPreviewProcess(cmd)
		_ = cmd.Wait()
		return nil, fmt.Errorf("studio is shutting down")
	}
	return previewStatus(run), nil
}

// SaveDetectedSessionPreviewConfig persists an auto-detected package.json
// fallback only after the frontend has shown and bound the exact command.
// Existing launch.json files are never overwritten by this convenience RPC.
func (s *Studio) SaveDetectedSessionPreviewConfig(projectID, sessionID, configuration, reviewedCommand string) (*SessionPreviewConfig, error) {
	p, session, err := s.projectSession(projectID, sessionID)
	if err != nil {
		return nil, err
	}
	dir, err := sessionWorkingDirectory(p, session)
	if err != nil {
		return nil, err
	}
	current, err := loadSessionPreviewConfig(dir)
	if err != nil {
		return nil, err
	}
	if current.Source != "detected" {
		return nil, fmt.Errorf("a preview configuration already exists or no server was detected")
	}
	var selected *PreviewServerConfiguration
	for index := range current.Configurations {
		if current.Configurations[index].Name == configuration {
			selected = &current.Configurations[index]
			break
		}
	}
	if selected == nil || selected.Command != reviewedCommand {
		return nil, fmt.Errorf("detected preview command changed after review")
	}
	persisted := previewConfigFile{Version: "0.0.1"}
	autoVerify := true
	persisted.AutoVerify = &autoVerify
	persisted.Configurations = append(persisted.Configurations, previewConfigEntry{
		Name: selected.Name, RuntimeExecutable: selected.RuntimeExecutable, RuntimeArgs: selected.RuntimeArgs, Port: selected.Port,
	})
	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := writeRootFileAtomic(root, filepath.FromSlash(current.Path), data, 0o600); err != nil {
		return nil, fmt.Errorf("save %s: %w", current.Path, err)
	}
	return loadSessionPreviewConfig(dir)
}

func resolvePreviewExecutable(dir, executable string) (string, error) {
	if !strings.ContainsAny(executable, `/\\`) {
		resolved, err := exec.LookPath(executable)
		if err != nil {
			return "", fmt.Errorf("preview executable %q was not found in PATH", executable)
		}
		return resolved, nil
	}
	candidate := executable
	absoluteConfigured := filepath.IsAbs(candidate)
	if !absoluteConfigured {
		candidate = filepath.Join(dir, filepath.FromSlash(candidate))
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve preview executable: %w", err)
	}
	if !absoluteConfigured {
		rel, err := filepath.Rel(dir, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("relative preview executable must stay inside the session workspace")
		}
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("preview executable is not an executable regular file")
	}
	// Windows has no POSIX execute bits — os.Stat reports 0666/0444 for every
	// regular file, so testing 0o111 there rejects every path-form executable.
	// Executability is carried by the extension instead.
	if runtime.GOOS == "windows" {
		if !windowsExecutableExtension(resolved) {
			return "", fmt.Errorf("preview executable is not an executable regular file")
		}
		return resolved, nil
	}
	if info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("preview executable is not an executable regular file")
	}
	return resolved, nil
}

// windowsExecutableExtension reports whether the path carries an extension
// Windows will actually execute. PATHEXT is honoured when present so a site
// that registers extra runnable extensions keeps working.
func windowsExecutableExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return false
	}
	candidates := []string{".exe", ".bat", ".cmd", ".com"}
	if pathExt := os.Getenv("PATHEXT"); pathExt != "" {
		candidates = candidates[:0]
		for _, entry := range strings.Split(pathExt, ";") {
			entry = strings.ToLower(strings.TrimSpace(entry))
			if entry != "" {
				candidates = append(candidates, entry)
			}
		}
	}
	return slices.Contains(candidates, ext)
}

func resolvePreviewWorkingDirectory(workspace, configured string) (string, error) {
	resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve preview workspace: %w", err)
	}
	candidate := resolvedWorkspace
	if configured != "" {
		candidate = filepath.Join(resolvedWorkspace, filepath.FromSlash(configured))
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve preview cwd: %w", err)
	}
	relative, err := filepath.Rel(resolvedWorkspace, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("preview cwd must stay inside the session workspace")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("preview cwd is not a directory")
	}
	return resolved, nil
}

func mergePreviewEnvironment(base []string, configured map[string]string, fixed ...string) []string {
	values := make(map[string]string, len(base)+len(configured)+len(fixed))
	for _, item := range base {
		if index := strings.IndexByte(item, '='); index > 0 {
			values[item[:index]] = item[index+1:]
		}
	}
	for key, value := range configured {
		values[key] = value
	}
	for _, item := range fixed {
		if index := strings.IndexByte(item, '='); index > 0 {
			values[item[:index]] = item[index+1:]
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func probePreviewTarget(target string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("configured preview URL is not reachable: %w", err)
	}
	_ = response.Body.Close()
	return nil
}

func (s *Studio) monitorPreviewServer(run *previewServerRun) {
	ready := make(chan bool, 1)
	go func() {
		deadline := time.Now().Add(previewReadyTimeout)
		previewURL := run.targetURL
		client := &http.Client{
			Timeout:       750 * time.Millisecond,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		}
		for time.Now().Before(deadline) {
			run.mu.RLock()
			active := run.state == "starting"
			run.mu.RUnlock()
			if !active {
				ready <- false
				return
			}
			request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, previewURL, nil)
			if response, err := client.Do(request); err == nil {
				_ = response.Body.Close()
				ready <- true
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		ready <- false
	}()
	wait := make(chan error, 1)
	go func() { wait <- run.cmd.Wait() }()
	select {
	case ok := <-ready:
		if ok {
			run.mu.Lock()
			if run.state == "starting" {
				run.state = "running"
			}
			run.mu.Unlock()
		} else {
			run.mu.Lock()
			if run.state == "starting" {
				run.state = "failed"
				run.err = fmt.Sprintf("server did not respond on 127.0.0.1:%d within %s", run.config.Port, previewReadyTimeout)
				run.cancel()
				_ = killPreviewProcess(run.cmd)
			}
			run.mu.Unlock()
		}
		err := <-wait
		finishPreviewRun(run, err)
	case err := <-wait:
		finishPreviewRun(run, err)
	}
}

func finishPreviewRun(run *previewServerRun, err error) {
	if run.proxy != nil {
		_ = run.proxy.Close()
	}
	closeStaticPreviewResources(run)
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.state == "stopped" || run.state == "failed" {
		return
	}
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "canceled") && !strings.Contains(strings.ToLower(err.Error()), "killed") {
		run.state = "failed"
		run.err = err.Error()
	} else {
		run.state = "stopped"
	}
}

func previewStatus(run *previewServerRun) *PreviewServerStatus {
	run.mu.RLock()
	status := &PreviewServerStatus{
		Configuration: run.config.Name,
		State:         run.state,
		URL:           run.proxyURL,
		BrowserURL:    run.browserURL,
		Port:          run.config.Port,
		StartedAt:     run.startedAt,
		Error:         run.err,
		BridgeToken:   run.bridgeToken,
	}
	if run.cmd != nil && run.cmd.Process != nil && (run.state == "starting" || run.state == "running") {
		status.PID = run.cmd.Process.Pid
	}
	run.mu.RUnlock()
	status.Logs = run.logs.String()
	return status
}

func (s *Studio) GetSessionPreviewServerStatus(projectID, sessionID, configuration string) (*PreviewServerStatus, error) {
	if _, _, err := s.projectSession(projectID, sessionID); err != nil {
		return nil, err
	}
	key := previewServerKey(projectID, sessionID, configuration)
	s.previewMu.Lock()
	run := s.previewServers[key]
	s.previewMu.Unlock()
	if run == nil {
		return &PreviewServerStatus{Configuration: configuration, State: "stopped"}, nil
	}
	return previewStatus(run), nil
}

func (s *Studio) sessionPreviewAutoVerifyRunning(projectID, sessionID string) bool {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	for _, run := range s.previewServers {
		run.mu.RLock()
		matches := run.projectID == projectID && run.sessionID == sessionID && run.state == "running" && run.autoVerify
		run.mu.RUnlock()
		if matches {
			return true
		}
	}
	return false
}

func (s *Studio) StopSessionPreviewServer(projectID, sessionID, configuration string) error {
	if _, _, err := s.projectSession(projectID, sessionID); err != nil {
		return err
	}
	key := previewServerKey(projectID, sessionID, configuration)
	s.previewMu.Lock()
	run := s.previewServers[key]
	s.previewMu.Unlock()
	if run == nil {
		return nil
	}
	run.mu.Lock()
	if run.state == "starting" || run.state == "running" {
		run.state = "stopped"
		if run.cancel != nil {
			run.cancel()
		}
		if run.cmd != nil && run.cmd.Process != nil {
			_ = killPreviewProcess(run.cmd)
		}
		if run.proxy != nil {
			_ = run.proxy.Close()
		}
		closeStaticPreviewResources(run)
	}
	run.mu.Unlock()
	return nil
}

func (s *Studio) stopPreviewServers(projectID, sessionID string, remove bool) {
	s.previewMu.Lock()
	for key := range s.previewStaticEpoch {
		projectMatch := projectID == "" || strings.HasPrefix(key, projectID+"\x00")
		sessionMatch := sessionID == "" || strings.HasPrefix(key, projectID+"\x00"+sessionID+"\x00")
		if projectMatch && sessionMatch {
			s.previewStaticEpoch[key]++
		}
	}
	for key, run := range s.previewServers {
		if (projectID == "" || run.projectID == projectID) && (sessionID == "" || run.sessionID == sessionID) {
			run.mu.Lock()
			run.state = "stopped"
			if run.cancel != nil {
				run.cancel()
			}
			if run.cmd != nil && run.cmd.Process != nil {
				_ = killPreviewProcess(run.cmd)
			}
			if run.proxy != nil {
				_ = run.proxy.Close()
			}
			closeStaticPreviewResources(run)
			run.mu.Unlock()
			if remove {
				delete(s.previewServers, key)
			}
		}
	}
	s.previewMu.Unlock()
}
