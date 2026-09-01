package auth

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// CodexReplicaMetadataKey stores the physical account's runtime replica policy.
	CodexReplicaMetadataKey = "codex_replica"

	// AttributeCodexReplicaGroup identifies runtime auths expanded from one physical account.
	AttributeCodexReplicaGroup       = "codex_replica_group"
	AttributeCodexReplicaIndex       = "codex_replica_index"
	AttributeCodexReplicaCount       = "codex_replica_count"
	AttributeCodexReplicaConcurrency = "codex_replica_concurrency"

	DefaultCodexReplicaCount       = 6
	DefaultCodexReplicaConcurrency = 10
	MaxCodexReplicaCount           = 64
	MaxCodexReplicaConcurrency     = 1000
)

// CodexReplicaConfig controls how one physical Codex OAuth file expands at runtime.
type CodexReplicaConfig struct {
	Enabled     bool `json:"enabled"`
	Count       int  `json:"count"`
	Concurrency int  `json:"concurrency"`
}

// ParseCodexReplicaConfig reads the optional policy while preserving disabled-by-default behavior.
func ParseCodexReplicaConfig(metadata map[string]any) (CodexReplicaConfig, error) {
	cfg := CodexReplicaConfig{
		Count:       DefaultCodexReplicaCount,
		Concurrency: DefaultCodexReplicaConcurrency,
	}
	if len(metadata) == 0 {
		return cfg, nil
	}
	raw, exists := metadata[CodexReplicaMetadataKey]
	if !exists || raw == nil {
		return cfg, nil
	}

	value, ok := raw.(map[string]any)
	if !ok {
		return CodexReplicaConfig{}, fmt.Errorf("%s must be an object", CodexReplicaMetadataKey)
	}
	if enabled, present := value["enabled"]; present {
		parsed, okBool := enabled.(bool)
		if !okBool {
			return CodexReplicaConfig{}, fmt.Errorf("%s.enabled must be a boolean", CodexReplicaMetadataKey)
		}
		cfg.Enabled = parsed
	}
	if count, present := value["count"]; present {
		parsed, okInt := codexReplicaInteger(count)
		if !okInt {
			return CodexReplicaConfig{}, fmt.Errorf("%s.count must be an integer", CodexReplicaMetadataKey)
		}
		cfg.Count = parsed
	}
	if concurrency, present := value["concurrency"]; present {
		parsed, okInt := codexReplicaInteger(concurrency)
		if !okInt {
			return CodexReplicaConfig{}, fmt.Errorf("%s.concurrency must be an integer", CodexReplicaMetadataKey)
		}
		cfg.Concurrency = parsed
	}
	if errValidate := cfg.Validate(); errValidate != nil {
		return CodexReplicaConfig{}, errValidate
	}
	return cfg, nil
}

// Validate bounds replica expansion and per-replica concurrency before publication.
func (c CodexReplicaConfig) Validate() error {
	if c.Count < 1 || c.Count > MaxCodexReplicaCount {
		return fmt.Errorf("%s.count must be between 1 and %d", CodexReplicaMetadataKey, MaxCodexReplicaCount)
	}
	if c.Concurrency < 1 || c.Concurrency > MaxCodexReplicaConcurrency {
		return fmt.Errorf("%s.concurrency must be between 1 and %d", CodexReplicaMetadataKey, MaxCodexReplicaConcurrency)
	}
	return nil
}

// ExpandCodexReplicas returns one unchanged auth unless an eligible Codex OAuth policy is enabled.
func ExpandCodexReplicas(auth *Auth) ([]*Auth, error) {
	if auth == nil {
		return nil, nil
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") || auth.AuthKind() != AuthKindOAuth {
		return []*Auth{auth}, nil
	}
	cfg, errConfig := ParseCodexReplicaConfig(auth.Metadata)
	if errConfig != nil {
		return nil, errConfig
	}
	if !cfg.Enabled {
		return []*Auth{auth}, nil
	}

	physicalID := strings.TrimSpace(auth.ID)
	if physicalID == "" {
		return nil, fmt.Errorf("Codex replica source auth ID is empty")
	}
	sharedIndex := auth.EnsureIndex()
	replicas := make([]*Auth, 0, cfg.Count)
	for index := 1; index <= cfg.Count; index++ {
		replica := auth.Clone()
		replica.ID = physicalID + "::replica:" + strconv.Itoa(index)
		// Egress is assigned from the unique runtime ID after expansion.
		replica.EgressIPv6 = ""
		replica.Index = sharedIndex
		replica.indexAssigned = sharedIndex != ""
		if replica.Attributes == nil {
			replica.Attributes = make(map[string]string)
		}
		replica.Attributes[AttributeCodexReplicaGroup] = physicalID
		replica.Attributes[AttributeCodexReplicaIndex] = strconv.Itoa(index)
		replica.Attributes[AttributeCodexReplicaCount] = strconv.Itoa(cfg.Count)
		replica.Attributes[AttributeCodexReplicaConcurrency] = strconv.Itoa(cfg.Concurrency)
		replicas = append(replicas, replica)
	}
	return replicas, nil
}

// NewCodexReplicaSource collapses a physical auth or one runtime replica into
// a clean physical source suitable for rebuilding the complete replica group.
func NewCodexReplicaSource(auth *Auth) (*Auth, bool) {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return nil, false
	}
	source := auth.Clone()
	if group, _, _, _, replica := CodexReplicaRuntimeInfo(auth); replica {
		source.ID = group
	}
	if source.Attributes == nil {
		source.Attributes = make(map[string]string)
	}
	for key := range source.Attributes {
		if isCodexReplicaRuntimeAttribute(key) {
			delete(source.Attributes, key)
		}
	}
	source.EgressIPv6 = ""
	source.Unavailable = false
	source.StatusMessage = ""
	source.Quota = QuotaState{}
	source.LastError = nil
	source.NextRetryAfter = time.Time{}
	source.ModelStates = nil
	source.Success = 0
	source.Failed = 0
	source.recentRequests = recentRequestRing{}
	if source.Disabled {
		source.Status = StatusDisabled
	} else {
		source.Status = StatusActive
	}
	return source, true
}

// CodexReplicaConfigForAuth returns the validated runtime policy for management projections.
func CodexReplicaConfigForAuth(auth *Auth) (CodexReplicaConfig, bool) {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return CodexReplicaConfig{}, false
	}
	cfg, errConfig := ParseCodexReplicaConfig(auth.Metadata)
	if errConfig != nil {
		return CodexReplicaConfig{}, false
	}
	return cfg, true
}

// CodexReplicaRuntimeInfo returns immutable group information carried by an expanded auth.
func CodexReplicaRuntimeInfo(auth *Auth) (group string, index, count, concurrency int, ok bool) {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") || len(auth.Attributes) == 0 {
		return "", 0, 0, 0, false
	}
	group = strings.TrimSpace(auth.Attributes[AttributeCodexReplicaGroup])
	index, indexOK := codexReplicaAttributeInteger(auth.Attributes[AttributeCodexReplicaIndex])
	count, countOK := codexReplicaAttributeInteger(auth.Attributes[AttributeCodexReplicaCount])
	concurrency, concurrencyOK := codexReplicaAttributeInteger(auth.Attributes[AttributeCodexReplicaConcurrency])
	if group == "" || !indexOK || !countOK || !concurrencyOK || index < 1 || index > count {
		return "", 0, 0, 0, false
	}
	return group, index, count, concurrency, true
}

// IsCodexReplicaLeader reports whether an expanded auth owns automatic token refresh.
func IsCodexReplicaLeader(auth *Auth) bool {
	_, index, _, _, ok := CodexReplicaRuntimeInfo(auth)
	return ok && index == 1
}

func codexReplicaInteger(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), int64(int(typed)) == typed
	case float64:
		parsed := int(typed)
		return parsed, float64(parsed) == typed
	case json.Number:
		parsed, errParse := typed.Int64()
		return int(parsed), errParse == nil && int64(int(parsed)) == parsed
	default:
		return 0, false
	}
}

func codexReplicaAttributeInteger(value string) (int, bool) {
	parsed, errParse := strconv.Atoi(strings.TrimSpace(value))
	return parsed, errParse == nil
}
