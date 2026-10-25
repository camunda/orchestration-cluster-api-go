package camunda

import (
	camundaapi "github.com/camunda/orchestration-cluster-api-go/v10/client"
)

// RebaseOperationServersForTesting exposes rebaseOperationServers to the
// external test package so the fail-closed contract on foreign URL templates
// can be pinned without standing up a full client.
func RebaseOperationServersForTesting(oc *camundaapi.Configuration, restAddress string, exact bool) (*camundaapi.Configuration, error) {
	cfg := &Config{RestAddress: restAddress, ExactRestAddress: exact}
	if err := rebaseOperationServers(oc, cfg); err != nil {
		return nil, err
	}
	return oc, nil
}
