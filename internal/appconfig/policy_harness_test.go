package appconfig

import (
	"net/http"
	"net/http/httptest"

	"github.com/pocketcontext-labs/pocketcontext-core/appserver"
)

func publicStatus(server *appserver.TestServer, path string) int {
	request := httptest.NewRequest(http.MethodGet, "https://drop.example.test"+path, nil)
	request.Host = "drop.example.test"
	response := httptest.NewRecorder()
	server.Public().ServeHTTP(response, request)
	return response.Code
}
