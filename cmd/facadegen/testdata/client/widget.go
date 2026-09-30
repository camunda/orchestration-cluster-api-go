// This is a test fixture for cmd/facadegen (a minimal stand-in for the generated
// camundaapi client). It lives under testdata/ so the Go toolchain ignores it; only
// facadegen's AST parser reads it.
package camundaapi

import (
	"context"
	"net/http"
)

type WidgetAPIService struct{}

type WidgetKey string

type Widget struct {
	Name string
}

type ApiGetWidgetRequest struct{}

func (a *WidgetAPIService) GetWidget(ctx context.Context, id WidgetKey) ApiGetWidgetRequest {
	return ApiGetWidgetRequest{}
}

func (a *WidgetAPIService) GetWidgetExecute(r ApiGetWidgetRequest) (*Widget, *http.Response, error) {
	return nil, nil, nil
}

type ApiDeleteWidgetRequest struct{}

func (a *WidgetAPIService) DeleteWidget(ctx context.Context, id WidgetKey) ApiDeleteWidgetRequest {
	return ApiDeleteWidgetRequest{}
}

func (a *WidgetAPIService) DeleteWidgetExecute(r ApiDeleteWidgetRequest) (*http.Response, error) {
	return nil, nil
}
