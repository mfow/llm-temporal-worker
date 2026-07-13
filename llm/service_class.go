package llm

import (
	"fmt"
)

// ServiceClass is the provider-neutral latency/cost class requested by a
// caller. It is intentionally closed: provider tier names never cross this
// boundary.
type ServiceClass string

const (
	ServiceClassEconomy  ServiceClass = "economy"
	ServiceClassStandard ServiceClass = "standard"
	ServiceClassPriority ServiceClass = "priority"
)

func (class ServiceClass) Valid() bool {
	switch class {
	case ServiceClassEconomy, ServiceClassStandard, ServiceClassPriority:
		return true
	default:
		return false
	}
}

// NormalizeServiceClass makes omission deterministic and rejects all values
// outside the public three-value enum. There is deliberately no
// provider-default value.
func NormalizeServiceClass(class ServiceClass) (ServiceClass, error) {
	if class == "" {
		return ServiceClassStandard, nil
	}
	if !class.Valid() {
		return "", fmt.Errorf("invalid service class %q: want economy, standard, or priority", class)
	}
	return class, nil
}

// ValidateServiceClassFallbacks validates an ordered explicit fallback list.
// A fallback authorizes another class but does not force the router to use it.
func ValidateServiceClassFallbacks(requested ServiceClass, fallbacks []ServiceClass) error {
	normalized, err := NormalizeServiceClass(requested)
	if err != nil {
		return err
	}
	seen := make(map[ServiceClass]struct{}, len(fallbacks))
	for index, class := range fallbacks {
		if !class.Valid() {
			return fmt.Errorf("service class fallback %d is invalid: %q", index, class)
		}
		if class == normalized {
			return fmt.Errorf("service class fallback %d repeats requested class %q", index, class)
		}
		if _, ok := seen[class]; ok {
			return fmt.Errorf("service class fallback %d repeats class %q", index, class)
		}
		seen[class] = struct{}{}
	}
	return nil
}
