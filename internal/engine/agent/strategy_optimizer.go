package agent

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ginkida/gokin-studio/internal/engine/fileutil"
	"github.com/ginkida/gokin-studio/internal/engine/logging"
)

const (
	MaxStrategyMetrics          = 500
	maxStrategyTaskTypes        = 256
	maxStrategyNameBytes        = 128
	maxStrategyTaskTypeBytes    = 256
	defaultStrategyTaskTypeName = "general"
)

// StrategyMetrics tracks performance metrics for a strategy.
type StrategyMetrics struct {
	StrategyName string         `json:"strategy_name"`
	SuccessCount int            `json:"success_count"`
	FailureCount int            `json:"failure_count"`
	TotalTime    time.Duration  `json:"total_time"`
	AvgDuration  time.Duration  `json:"avg_duration"`
	LastUsed     time.Time      `json:"last_used"`
	TaskTypes    map[string]int `json:"task_types"` // TaskType -> count
}

// SuccessRate returns the success rate as a percentage.
func (sm *StrategyMetrics) SuccessRate() float64 {
	if sm == nil {
		return 0.5
	}
	successes := nonNegativeInt(sm.SuccessCount)
	failures := nonNegativeInt(sm.FailureCount)
	total := float64(successes) + float64(failures)
	if total == 0 {
		return 0.5 // Unknown, return neutral
	}
	return float64(successes) / total
}

// clone returns a deep copy of the metrics.
func (sm *StrategyMetrics) clone() *StrategyMetrics {
	if sm == nil {
		return nil
	}
	c := *sm
	if sm.TaskTypes != nil {
		c.TaskTypes = make(map[string]int, len(sm.TaskTypes))
		for k, v := range sm.TaskTypes {
			c.TaskTypes[k] = v
		}
	}
	return &c
}

// StrategyOptimizer analyzes and optimizes agent strategies based on outcomes.
type StrategyOptimizer struct {
	metrics   map[string]*StrategyMetrics // strategy name -> metrics
	configDir string
	mu        sync.RWMutex
	writer    fileutil.LatestFileWriter
}

// NewStrategyOptimizer creates a new strategy optimizer.
func NewStrategyOptimizer(configDir string) *StrategyOptimizer {
	so := &StrategyOptimizer{
		metrics:   make(map[string]*StrategyMetrics),
		configDir: configDir,
	}

	// Load existing metrics
	if err := so.load(); err != nil {
		logging.Debug("failed to load strategy metrics", "error", err)
	}

	return so
}

// storagePath returns the path to the metrics file.
func (so *StrategyOptimizer) storagePath() string {
	return filepath.Join(so.configDir, "memory", "strategy_metrics.json")
}

// load loads metrics from disk.
func (so *StrategyOptimizer) load() error {
	data, err := fileutil.ReadRegularFileLimited(so.storagePath(), maxOptimizerStoreFileBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var metrics map[string]*StrategyMetrics
	if err := json.Unmarshal(data, &metrics); err != nil {
		return err
	}
	if metrics == nil {
		metrics = make(map[string]*StrategyMetrics)
	}
	normalized := make(map[string]*StrategyMetrics, minInt(len(metrics), MaxStrategyMetrics))
	names := make([]string, 0, len(metrics))
	for name := range metrics {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		canonicalName, ok := normalizeStrategyLabel(name, maxStrategyNameBytes, false)
		if !ok || canonicalName == "" {
			continue
		}
		metric := normalizeStrategyMetrics(canonicalName, metrics[name])
		if metric == nil {
			continue
		}
		if existing := normalized[canonicalName]; existing == nil || metric.LastUsed.After(existing.LastUsed) {
			normalized[canonicalName] = metric
		}
	}
	so.metrics = normalized
	so.evictOldest(MaxStrategyMetrics)
	return nil
}

// save serializes metrics under the caller's lock and returns the snapshot.
// Caller must hold so.mu (read or write lock).
func (so *StrategyOptimizer) save() ([]byte, error) {
	data, err := json.MarshalIndent(so.metrics, "", "  ")
	if err != nil {
		return nil, err
	}
	return data, nil
}

// writeSnapshot writes pre-serialized data to disk without holding any locks.
func (so *StrategyOptimizer) writeSnapshot(data []byte) error {
	dir := filepath.Dir(so.storagePath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return so.writer.Write(so.storagePath(), data, 0o600)
}

func (so *StrategyOptimizer) scheduleSnapshot(data []byte) {
	dir := filepath.Dir(so.storagePath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		logging.Debug("failed to create strategy metrics directory", "error", err)
		return
	}
	so.writer.Schedule(so.storagePath(), data, 0o600, func(err error) {
		if err != nil {
			logging.Debug("failed to save strategy metrics", "error", err)
		}
	})
}

// RecordExecution records the outcome of a strategy execution.
func (so *StrategyOptimizer) RecordExecution(strategyName string, taskType string, success bool, duration time.Duration) {
	strategyName, ok := normalizeStrategyLabel(strategyName, maxStrategyNameBytes, false)
	if !ok || strategyName == "" {
		return
	}
	taskType, ok = normalizeStrategyLabel(taskType, maxStrategyTaskTypeBytes, true)
	if !ok {
		return
	}
	if duration < 0 {
		duration = 0
	}

	so.mu.Lock()
	defer so.mu.Unlock()
	if so.metrics == nil {
		so.metrics = make(map[string]*StrategyMetrics)
	}

	metrics, ok := so.metrics[strategyName]
	if !ok || metrics == nil {
		if !ok && len(so.metrics) >= MaxStrategyMetrics {
			so.evictOldest(MaxStrategyMetrics - 1)
		}
		metrics = &StrategyMetrics{
			StrategyName: strategyName,
			TaskTypes:    make(map[string]int),
		}
		so.metrics[strategyName] = metrics
	}
	if metrics.TaskTypes == nil {
		metrics.TaskTypes = make(map[string]int)
	}

	if success {
		metrics.SuccessCount = saturatingIncrement(metrics.SuccessCount)
	} else {
		metrics.FailureCount = saturatingIncrement(metrics.FailureCount)
	}

	metrics.TotalTime = saturatingDurationAdd(metrics.TotalTime, duration)
	total := saturatingCountSum(metrics.SuccessCount, metrics.FailureCount)
	if total > 0 {
		metrics.AvgDuration = metrics.TotalTime / time.Duration(total)
	}
	metrics.LastUsed = time.Now()
	metrics.TaskTypes[taskType] = saturatingIncrement(metrics.TaskTypes[taskType])
	trimStrategyTaskTypes(metrics.TaskTypes, maxStrategyTaskTypes)

	// Snapshot data under lock, write to disk asynchronously
	snapshot, err := so.save()
	if err != nil {
		logging.Debug("failed to serialize strategy metrics", "error", err)
		return
	}
	so.scheduleSnapshot(snapshot)
}

// GetSuccessRate returns the success rate for a strategy.
func (so *StrategyOptimizer) GetSuccessRate(strategyName string) float64 {
	strategyName, ok := normalizeStrategyLabel(strategyName, maxStrategyNameBytes, false)
	if !ok || strategyName == "" {
		return 0.5
	}
	so.mu.RLock()
	defer so.mu.RUnlock()

	metrics, ok := so.metrics[strategyName]
	if !ok || metrics == nil {
		return 0.5 // Unknown strategy, return neutral
	}

	return metrics.SuccessRate()
}

// GetMetrics returns a copy of the metrics for a strategy.
func (so *StrategyOptimizer) GetMetrics(strategyName string) (*StrategyMetrics, bool) {
	strategyName, ok := normalizeStrategyLabel(strategyName, maxStrategyNameBytes, false)
	if !ok || strategyName == "" {
		return nil, false
	}
	so.mu.RLock()
	defer so.mu.RUnlock()

	metrics, ok := so.metrics[strategyName]
	if !ok || metrics == nil {
		return nil, false
	}
	return metrics.clone(), true
}

// RecommendStrategy recommends the best strategy for a task type.
func (so *StrategyOptimizer) RecommendStrategy(taskType string) string {
	taskType, ok := normalizeStrategyLabel(taskType, maxStrategyTaskTypeBytes, true)
	if !ok {
		taskType = defaultStrategyTaskTypeName
	}
	so.mu.RLock()
	defer so.mu.RUnlock()

	type strategyScore struct {
		name  string
		score float64
	}

	var scores []strategyScore

	for name, metrics := range so.metrics {
		if metrics == nil {
			continue
		}
		// Calculate a score based on:
		// 1. Success rate (most important)
		// 2. Experience with this task type
		// 3. Recency of use

		baseScore := metrics.SuccessRate()

		// Boost score if this strategy has been used for this task type
		taskTypeCount := metrics.TaskTypes[taskType]
		total := float64(nonNegativeInt(metrics.SuccessCount)) + float64(nonNegativeInt(metrics.FailureCount))
		if taskTypeCount > 0 && total > 0 {
			// More experience = higher confidence in the score
			experienceBoost := float64(taskTypeCount) / total
			if experienceBoost > 1 {
				experienceBoost = 1
			}
			baseScore += experienceBoost * 0.2 // Up to 20% boost
		}

		// Small penalty for strategies not used recently
		daysSinceUse := time.Since(metrics.LastUsed).Hours() / 24
		if daysSinceUse > 30 {
			baseScore *= 0.9 // 10% penalty for old strategies
		}

		scores = append(scores, strategyScore{name: name, score: baseScore})
	}

	if len(scores) == 0 {
		return "general" // Default fallback
	}

	// Sort by score (highest first)
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].score != scores[j].score {
			return scores[i].score > scores[j].score
		}
		return scores[i].name < scores[j].name
	})

	return scores[0].name
}

// GetAllMetrics returns a deep copy of all strategy metrics.
func (so *StrategyOptimizer) GetAllMetrics() map[string]*StrategyMetrics {
	so.mu.RLock()
	defer so.mu.RUnlock()

	result := make(map[string]*StrategyMetrics, len(so.metrics))
	for k, v := range so.metrics {
		if clone := v.clone(); clone != nil {
			result[k] = clone
		}
	}
	return result
}

// GetTopStrategies returns deep copies of the top N strategies by success rate.
func (so *StrategyOptimizer) GetTopStrategies(n int) []*StrategyMetrics {
	if n <= 0 {
		return nil
	}
	so.mu.RLock()
	defer so.mu.RUnlock()

	metrics := make([]*StrategyMetrics, 0, len(so.metrics))
	for _, m := range so.metrics {
		if clone := m.clone(); clone != nil {
			metrics = append(metrics, clone)
		}
	}

	sort.Slice(metrics, func(i, j int) bool {
		left, right := metrics[i].SuccessRate(), metrics[j].SuccessRate()
		if left != right {
			return left > right
		}
		return metrics[i].StrategyName < metrics[j].StrategyName
	})

	if n > len(metrics) {
		n = len(metrics)
	}

	return metrics[:n]
}

func (so *StrategyOptimizer) evictOldest(maxSize int) {
	if maxSize < 0 {
		maxSize = 0
	}
	if len(so.metrics) <= maxSize {
		return
	}
	type entry struct {
		name     string
		lastUsed time.Time
	}
	entries := make([]entry, 0, len(so.metrics))
	for name, metrics := range so.metrics {
		var lastUsed time.Time
		if metrics != nil {
			lastUsed = metrics.LastUsed
		}
		entries = append(entries, entry{name: name, lastUsed: lastUsed})
	}
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].lastUsed.Equal(entries[j].lastUsed) {
			return entries[i].lastUsed.Before(entries[j].lastUsed)
		}
		return entries[i].name < entries[j].name
	})
	for i := 0; i < len(so.metrics)-maxSize; i++ {
		delete(so.metrics, entries[i].name)
	}
}

func normalizeStrategyMetrics(name string, metrics *StrategyMetrics) *StrategyMetrics {
	if metrics == nil {
		return nil
	}
	normalized := metrics.clone()
	normalized.StrategyName = name
	normalized.SuccessCount = nonNegativeInt(normalized.SuccessCount)
	normalized.FailureCount = nonNegativeInt(normalized.FailureCount)
	if normalized.TotalTime < 0 {
		normalized.TotalTime = 0
	}
	total := saturatingCountSum(normalized.SuccessCount, normalized.FailureCount)
	if total > 0 {
		normalized.AvgDuration = normalized.TotalTime / time.Duration(total)
	} else {
		normalized.AvgDuration = 0
	}
	taskTypes := make(map[string]int)
	for taskType, count := range normalized.TaskTypes {
		canonical, ok := normalizeStrategyLabel(taskType, maxStrategyTaskTypeBytes, true)
		if !ok || count <= 0 {
			continue
		}
		taskTypes[canonical] = saturatingCountSum(taskTypes[canonical], count)
	}
	trimStrategyTaskTypes(taskTypes, maxStrategyTaskTypes)
	normalized.TaskTypes = taskTypes
	return normalized
}

func normalizeStrategyLabel(value string, maxBytes int, blankAsGeneral bool) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" && blankAsGeneral {
		return defaultStrategyTaskTypeName, true
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) || len(value) > maxBytes {
		return "", false
	}
	return value, true
}

func trimStrategyTaskTypes(taskTypes map[string]int, maxSize int) {
	if len(taskTypes) <= maxSize {
		return
	}
	type entry struct {
		name  string
		count int
	}
	entries := make([]entry, 0, len(taskTypes))
	for name, count := range taskTypes {
		entries = append(entries, entry{name: name, count: count})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].count != entries[j].count {
			return entries[i].count < entries[j].count
		}
		return entries[i].name < entries[j].name
	})
	for i := 0; i < len(taskTypes)-maxSize; i++ {
		delete(taskTypes, entries[i].name)
	}
}

func nonNegativeInt(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func saturatingIncrement(value int) int {
	value = nonNegativeInt(value)
	maxInt := int(^uint(0) >> 1)
	if value == maxInt {
		return maxInt
	}
	return value + 1
}

func saturatingCountSum(left, right int) int {
	left = nonNegativeInt(left)
	right = nonNegativeInt(right)
	maxInt := int(^uint(0) >> 1)
	if left > maxInt-right {
		return maxInt
	}
	return left + right
}

func saturatingDurationAdd(current, delta time.Duration) time.Duration {
	if current < 0 {
		current = 0
	}
	if delta <= 0 {
		return current
	}
	if current > time.Duration(math.MaxInt64)-delta {
		return time.Duration(math.MaxInt64)
	}
	return current + delta
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

// Clear removes all metrics.
func (so *StrategyOptimizer) Clear() error {
	so.mu.Lock()
	defer so.mu.Unlock()

	so.metrics = make(map[string]*StrategyMetrics)
	snapshot, err := so.save()
	if err != nil {
		return err
	}
	return so.writeSnapshot(snapshot)
}
