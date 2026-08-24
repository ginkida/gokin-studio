package studio

import (
	"strings"
	"testing"
)

// Every model the product offers must be priceable. A catalog entry without a
// pricing row shows no cost anywhere AND silently disables strict budget
// enforcement, because an unpriced turn contributes $0 to the cumulative total.
func TestLookupPricingCoversEveryCatalogModel(t *testing.T) {
	for _, provider := range studioProviderCatalog {
		for _, model := range provider.Models {
			if LookupPricing(provider.ID, model) == (ModelPricing{}) {
				t.Errorf("no pricing row for catalog model %s/%s — cost display and budget enforcement both go silent",
					provider.ID, model)
			}
		}
	}
}

// Studio deliberately accepts forward-compatible model ids so a newly released
// model discovered on the account can be selected before the catalog ships an
// entry for it (isFutureStudioModelID). The pricing table cannot price those,
// so EstimateCost returns 0, bumpTotalCostUSD short-circuits, the cumulative
// total never grows, and the pre-flight check never trips.
//
// That combination turns a hard spend cap into a no-op while the UI still
// presents it as enforced — the exact runaway the feature exists to stop. It
// must fail closed and say why instead.
func TestSendMessage_RefusesEnforcedBudgetWhenModelHasNoPricing(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{text: "hello"}}}
	p, rec := newTestProject(t, mc, nil)
	// Accepted by validateStudioProviderModelRuntime as a future Kimi id, but
	// absent from the pricing table.
	p.Provider, p.Model = "kimi", "k4"
	if LookupPricing(p.Provider, p.Model) != (ModelPricing{}) {
		t.Skip("k4 gained a pricing row; pick another unpriced future id for this fixture")
	}
	p.cachedTotalCostUSD = 0
	p.costSeeded = true
	p.BudgetUSD = 5.00
	p.EnforceBudget = true

	runAgent(p, "spend without a measurable price")

	errs := rec.find(EventChatError)
	if len(errs) == 0 {
		t.Fatal("expected chat:error: an unpriced model makes the budget unenforceable")
	}
	te, _ := errs[0].data.(ChatTextEvent)
	if !strings.Contains(te.Text, "k4") || !strings.Contains(strings.ToLower(te.Text), "pricing") {
		t.Errorf("error text = %q, want it to name the model and the missing pricing", te.Text)
	}
	if mc.callCount != 0 {
		t.Errorf("a paid turn ran under a budget that cannot be enforced: callCount=%d", mc.callCount)
	}
}

// The guard is scoped to the safety promise. Without strict enforcement the
// user never asked for a hard cap, so an unpriced model must still run — it
// simply reports no cost, which is what it did before.
func TestSendMessage_UnpricedModelStillRunsWithoutEnforcement(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{text: "ok"}}}
	p, rec := newTestProject(t, mc, nil)
	p.Provider, p.Model = "kimi", "k4"
	p.BudgetUSD = 5.00
	p.EnforceBudget = false

	runAgent(p, "go")

	if len(rec.find(EventChatError)) != 0 {
		t.Error("an unpriced model must not be blocked when the budget is not enforced")
	}
	if len(rec.find(EventChatText)) == 0 {
		t.Error("expected the turn to run normally")
	}
}

// A priced model under an enforced budget is untouched by the new guard.
func TestSendMessage_PricedModelUnaffectedByPricingGuard(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{text: "ok"}}}
	p, rec := newTestProject(t, mc, nil)
	p.Provider, p.Model = "kimi", "k3"
	p.cachedTotalCostUSD = 1.00
	p.costSeeded = true
	p.BudgetUSD = 5.00
	p.EnforceBudget = true

	runAgent(p, "go")

	if len(rec.find(EventChatError)) != 0 {
		t.Error("a priced model under budget must not be blocked")
	}
	if len(rec.find(EventChatText)) == 0 {
		t.Error("expected the turn to run normally")
	}
}

// The delegation pre-flight exists so a cross-project run cannot start work the
// target would immediately refuse, and its comment says it mirrors the agent
// loop's own check. It has to mirror the pricing gate too: otherwise a project
// whose budget is unenforceable blocks its own chat while still accepting paid
// work pushed in from another project.
func TestDelegationRefusedWhenTargetBudgetCannotBePriced(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	s.mu.RLock()
	target := s.projects[to.ID]
	s.mu.RUnlock()
	target.mu.Lock()
	target.EnforceBudget = true
	target.BudgetUSD = 100.00 // nowhere near exhausted: only the pricing gap refuses
	target.Provider, target.Model = "kimi", "k4"
	target.mu.Unlock()

	_, err := s.StartDelegation(from.ID, "default", to.ID, "run", "", "expensive work")
	if DelegationErrorType(err) != DelegationErrorBudget {
		t.Fatalf("error_type = %q, want %q (%v)", DelegationErrorType(err), DelegationErrorBudget, err)
	}
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "pricing") {
		t.Errorf("error = %v, want it to name the missing pricing", err)
	}
	assertNoDelegationSideEffects(t, s, to)
}
