package httpapi

import "net/http"

// OperationID returns the operationId of the contract operation r is
// addressed to, or false when r matches none. It lets instrumentation
// outside this package label requests by operation, a bounded set, rather
// than by path, which carries IDs.
func OperationID(r *http.Request) (string, bool) {
	route, _, err := contractRouter.FindRoute(r)
	if err != nil || route.Operation == nil || route.Operation.OperationID == "" {
		return "", false
	}
	return route.Operation.OperationID, true
}
