package workflows

import "context"

// TemporalClient mimics the shape of go.temporal.io/sdk/client. Only
// the methods we need for the fixture are present.
type TemporalClient interface {
	ExecuteWorkflow(ctx context.Context, opts any, workflow any, args ...any) (any, error)
	RegisterWorkflow(workflow any)
}

// startFoodLogging fires FoodLoggingWorkflow by passing it as a
// value. The workflow name never appears followed by `(`, so the
// direct-call regex misses it — hence the value-reference fallback.
func startFoodLogging(ctx context.Context, tc TemporalClient, meal string) error {
	_, err := tc.ExecuteWorkflow(ctx, nil, FoodLoggingWorkflow, meal)
	return err
}

// registerAll wires every workflow function into the client under
// various value-passed idioms: direct register, dispatch map, and go
// statement.
func registerAll(ctx context.Context, tc TemporalClient) {
	tc.RegisterWorkflow(FoodLoggingWorkflow)
	tc.RegisterWorkflow(StepCounterWorkflow)

	dispatch := map[string]any{
		"hydrate": HydrationWorkflow,
	}
	_ = dispatch

	go FoodLoggingWorkflow(ctx, "breakfast")
}
