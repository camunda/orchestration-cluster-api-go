package camundaapi

type APIClient struct {
	WidgetAPI *WidgetAPIService
}

type Configuration struct{}

func NewConfiguration() *Configuration { return &Configuration{} }

type contextKey string

var ContextAccessToken = contextKey("accesstoken")
