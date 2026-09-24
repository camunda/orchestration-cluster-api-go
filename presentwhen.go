package camunda

import "fmt"

// leaseCouplingKey names the one x-present-when coupling this runtime enforces.
//
// A coupling in presentWhenCouplings that is absent from enforcedCouplings is one the
// specification declares and the workers silently ignore; TestEveryPresentWhenCouplingIsEnforced
// is what makes that visible rather than letting it pass as an unread field.
const leaseCouplingKey = "ActivatedJobResult.jobLeaseToken"

var enforcedCouplings = map[string]bool{
	leaseCouplingKey: true,
}

func (c presentWhenCoupling) key() string { return c.ResponseSchema + "." + c.ResponseField }

// couplingByKey returns the declared coupling for key.
func couplingByKey(key string) (presentWhenCoupling, bool) {
	for _, c := range presentWhenCouplings {
		if c.key() == key {
			return c, true
		}
	}
	return presentWhenCoupling{}, false
}

// requireLeasePresence enforces the lease coupling at the activation boundary, which
// is the last point at which the server's answer can still be rejected as a whole.
// requested reports whether the activation set the lease flag; token is what came back.
func requireLeasePresence(requested bool, token string) error {
	if !requested || token != "" {
		return nil
	}
	flag := "the lease flag"
	if c, ok := couplingByKey(leaseCouplingKey); ok {
		flag = fmt.Sprintf("%q", c.RequestFlag)
	}
	return fmt.Errorf("%w: the specification declares %s present whenever an activation sets %s, "+
		"so this server does not support job leases and its commands cannot be fenced",
		ErrLeaseNotHonored, leaseCouplingKey, flag)
}
