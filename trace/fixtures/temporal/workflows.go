// Package workflows is a Temporal-style fixture used by
// trace/value_refs tests. It defines three workflow functions whose
// usages in consumer.go are value-passed (no parentheses at the call
// site), which is exactly the scenario the value-reference post-pass
// should surface.
//
// Do not import this package from production code — it is a fixture.
package workflows

import "context"

// FoodLoggingWorkflow is the primary target of the trace test.
func FoodLoggingWorkflow(ctx context.Context, meal string) error {
	return nil
}

// StepCounterWorkflow is a secondary workflow referenced by name.
func StepCounterWorkflow(ctx context.Context, user string) error {
	return nil
}

// HydrationWorkflow is referenced via a dispatch map.
func HydrationWorkflow(ctx context.Context) error {
	return nil
}
