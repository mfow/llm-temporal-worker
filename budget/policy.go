package budget

import (
	"fmt"
	"strings"

	"github.com/mfow/llm-temporal-worker/llm"
	"github.com/mfow/llm-temporal-worker/routing"
)

type MatchContext struct {
	Tenant       string
	Project      string
	Actor        string
	Environment  string
	LogicalModel string
	EndpointID   string
	ServiceClass llm.ServiceClass
}

type Matcher struct {
	Tenant       string
	Project      string
	ActorPrefix  string
	Environment  string
	LogicalModel string
	EndpointID   string
	ServiceClass llm.ServiceClass
}

type Policy struct {
	ID      string
	Match   Matcher
	Windows []Window
}

func (matcher Matcher) Matches(value MatchContext) bool {
	if !matchExact(matcher.Tenant, value.Tenant) || !matchExact(matcher.Project, value.Project) || !matchExact(matcher.Environment, value.Environment) || !matchExact(matcher.LogicalModel, value.LogicalModel) || !matchExact(matcher.EndpointID, value.EndpointID) {
		return false
	}
	if matcher.ActorPrefix != "" && !strings.HasPrefix(value.Actor, matcher.ActorPrefix) {
		return false
	}
	if matcher.ServiceClass != "" && matcher.ServiceClass != value.ServiceClass {
		return false
	}
	return true
}

func matchExact(rule, value string) bool {
	if rule == "" || rule == "*" {
		return true
	}
	return value != "" && rule == value
}

func (policy Policy) Validate(maxBuckets int) error {
	if policy.ID == "" || len(policy.Windows) == 0 {
		return fmt.Errorf("budget policy requires ID and windows")
	}
	for index := range policy.Windows {
		if err := policy.Windows[index].Validate(maxBuckets); err != nil {
			return fmt.Errorf("policy %s window %d: %w", policy.ID, index, err)
		}
	}
	return nil
}

func ContextFor(request llm.Request, candidate routing.Candidate, environment string) MatchContext {
	class := candidate.AttemptedClass
	if class == "" {
		class = request.ServiceClass
	}
	return MatchContext{Tenant: request.Context.Tenant, Project: request.Context.Project, Actor: request.Context.Actor, Environment: environment, LogicalModel: request.Model, EndpointID: candidate.EndpointID, ServiceClass: class}
}
