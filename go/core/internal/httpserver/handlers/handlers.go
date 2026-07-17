package handlers

import (
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kagent-dev/kagent/go/api/database"
	"github.com/kagent-dev/kagent/go/core/internal/controller/reconciler"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	"github.com/kagent-dev/kagent/go/core/pkg/sandboxbackend"
	"github.com/kagent-dev/kagent/go/core/pkg/sandboxbackend/substrate"
)

// ClusterAccess bundles cache-bypassing cluster access for the cluster
// query/tool endpoints. The uncached client reads straight from the API
// server, so results are not limited by the manager cache's namespace
// scoping and arbitrary kinds don't inflate the informer cache.
type ClusterAccess struct {
	// Client is an uncached controller-runtime client.
	Client client.Client
	// Clientset serves subresources the generic client can't (pods/log).
	Clientset kubernetes.Interface
	// RESTMapper resolves user-supplied kind/resource strings to GVKs.
	RESTMapper meta.RESTMapper
	// WriteEnabled gates the destructive cluster endpoints (apply, delete,
	// scale, rollout-restart). The controller's own RBAC is broad, so the
	// gate is an explicit deployment opt-in rather than an RBAC side effect.
	WriteEnabled bool
}

// Handlers holds all the HTTP handler components
type Handlers struct {
	KubeClient          client.Client
	AgentHarnessGateway *AgentHarnessGatewayConfig
	// AgentHarnessSessionActor creates/suspends the per-session substrate actors
	// that back each AgentHarness chat session.
	AgentHarnessSessionActor *substrate.AgentHarnessSessionActorBackend

	Health              *HealthHandler
	ModelConfig         *ModelConfigHandler
	Model               *ModelHandler
	ModelProviderConfig *ModelProviderConfigHandler
	Sessions            *SessionsHandler
	Agents              *AgentsHandler
	Tools               *ToolsHandler
	ToolServers         *ToolServersHandler
	ToolServerTypes     *ToolServerTypesHandler
	Memory              *MemoryHandler
	Feedback            *FeedbackHandler
	Namespaces          *NamespacesHandler
	Resources           *ResourcesHandler
	ClusterQuery        *ClusterQueryHandler
	Jenkins             *JenkinsHandler
	PromptTemplates     *PromptTemplatesHandler
	Tasks               *TasksHandler
	Checkpoints         *CheckpointsHandler
	CrewAI              *CrewAIHandler
	CurrentUser         *CurrentUserHandler
	Substrate           *SubstrateHandler
}

// Base holds common dependencies for all handlers
type Base struct {
	KubeClient         client.Client
	DefaultModelConfig types.NamespacedName
	DatabaseService    database.Client
	Authorizer         auth.Authorizer // Interface for authorization checks
	ProxyURL           string
	WatchedNamespaces  []string
	SandboxBackend     sandboxbackend.Backend
	MCPEgressPlaintext bool
	// Cluster provides cache-bypassing cluster access; nil in tests that
	// don't exercise the cluster query endpoints.
	Cluster *ClusterAccess
}

// clusterReader returns the best client for cluster-wide reads: the uncached
// cluster-access client when configured, otherwise the (possibly cache-scoped)
// manager client.
func (b *Base) clusterReader() client.Client {
	if b.Cluster != nil && b.Cluster.Client != nil {
		return b.Cluster.Client
	}
	return b.KubeClient
}

// NewHandlers creates a new Handlers instance with all handler components.
func NewHandlers(
	kubeClient client.Client,
	defaultModelConfig types.NamespacedName,
	dbService database.Client,
	watchedNamespaces []string,
	authorizer auth.Authorizer,
	proxyURL string,
	rcnclr reconciler.KagentReconciler,
	sandboxBackend sandboxbackend.Backend,
	agentHarnessGateway *AgentHarnessGatewayConfig,
	substrateAteClient *substrate.Client,
	mcpEgressPlaintext bool,
	substrateSandboxActorBackend *substrate.SandboxAgentActorBackend,
	agentHarnessSessionActorBackend *substrate.AgentHarnessSessionActorBackend,
	clusterAccess *ClusterAccess,
) *Handlers {
	base := &Base{
		KubeClient:         kubeClient,
		DefaultModelConfig: defaultModelConfig,
		DatabaseService:    dbService,
		Authorizer:         authorizer,
		ProxyURL:           proxyURL,
		WatchedNamespaces:  watchedNamespaces,
		SandboxBackend:     sandboxBackend,
		MCPEgressPlaintext: mcpEgressPlaintext,
		Cluster:            clusterAccess,
	}

	return &Handlers{
		KubeClient:               kubeClient,
		AgentHarnessGateway:      agentHarnessGateway,
		AgentHarnessSessionActor: agentHarnessSessionActorBackend,
		Health:                   NewHealthHandler(),
		ModelConfig:              NewModelConfigHandler(base),
		Model:                    NewModelHandler(base),
		ModelProviderConfig:      NewModelProviderConfigHandler(base, rcnclr),
		Sessions:                 NewSessionsHandler(base, substrateSandboxActorBackend),
		Agents:                   NewAgentsHandler(base),
		Tools:                    NewToolsHandler(base),
		ToolServers:              NewToolServersHandler(base),
		ToolServerTypes:          NewToolServerTypesHandler(base),
		Memory:                   NewMemoryHandler(base),
		Feedback:                 NewFeedbackHandler(base),
		Namespaces:               NewNamespacesHandler(base),
		Resources:                NewResourcesHandler(base),
		ClusterQuery:             NewClusterQueryHandler(base),
		Jenkins:                  NewJenkinsHandler(base),
		PromptTemplates:          NewPromptTemplatesHandler(base),
		Tasks:                    NewTasksHandler(base),
		Checkpoints:              NewCheckpointsHandler(base),
		CrewAI:                   NewCrewAIHandler(base),
		CurrentUser:              NewCurrentUserHandler(),
		Substrate:                NewSubstrateHandler(base, substrateAteClient),
	}
}
