package agent_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kagent-dev/kagent/go/api/v1alpha2"
	translator "github.com/kagent-dev/kagent/go/core/internal/controller/translator/agent"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	schemev1 "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func Test_AdkApiTranslator_BuiltinTools(t *testing.T) {
	scheme := schemev1.Scheme
	require.NoError(t, v1alpha2.AddToScheme(scheme))

	modelConfig := &v1alpha2.ModelConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-model",
			Namespace: "default",
		},
		Spec: v1alpha2.ModelConfigSpec{
			Model:    "gpt-4",
			Provider: v1alpha2.ModelProviderOpenAI,
		},
	}

	makeAgent := func(tools []*v1alpha2.Tool) *v1alpha2.Agent {
		return &v1alpha2.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
			Spec: v1alpha2.AgentSpec{
				Type:        v1alpha2.AgentType_Declarative,
				Description: "Test agent",
				Declarative: &v1alpha2.DeclarativeAgentSpec{
					SystemMessage: "You are a test agent",
					ModelConfig:   "test-model",
					Tools:         tools,
				},
			},
		}
	}

	tests := []struct {
		name         string
		agent        *v1alpha2.Agent
		wantBuiltins []string
	}{
		{
			name:         "no builtin tools",
			agent:        makeAgent(nil),
			wantBuiltins: nil,
		},
		{
			name: "bash and file tools",
			agent: makeAgent([]*v1alpha2.Tool{
				{
					Type: v1alpha2.ToolProviderType_Builtin,
					Builtin: &v1alpha2.BuiltinTool{
						Names: []v1alpha2.BuiltinToolName{
							v1alpha2.BuiltinToolName_Bash,
							v1alpha2.BuiltinToolName_ReadFile,
						},
					},
				},
			}),
			wantBuiltins: []string{"bash", "read_file"},
		},
		{
			name: "duplicate names across entries are deduplicated",
			agent: makeAgent([]*v1alpha2.Tool{
				{
					Type: v1alpha2.ToolProviderType_Builtin,
					Builtin: &v1alpha2.BuiltinTool{
						Names: []v1alpha2.BuiltinToolName{
							v1alpha2.BuiltinToolName_Bash,
							v1alpha2.BuiltinToolName_WriteFile,
						},
					},
				},
				{
					Type: v1alpha2.ToolProviderType_Builtin,
					Builtin: &v1alpha2.BuiltinTool{
						Names: []v1alpha2.BuiltinToolName{
							v1alpha2.BuiltinToolName_Bash,
							v1alpha2.BuiltinToolName_EditFile,
						},
					},
				},
			}),
			wantBuiltins: []string{"bash", "write_file", "edit_file"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kubeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects([]client.Object{modelConfig.DeepCopy()}...).
				Build()

			defaultModel := types.NamespacedName{Namespace: "default", Name: "test-model"}
			trans := translator.NewAdkApiTranslator(kubeClient, defaultModel, nil, "", nil)
			outputs, err := translator.TranslateAgent(context.Background(), trans, tt.agent)

			require.NoError(t, err)
			require.NotNil(t, outputs)
			require.NotNil(t, outputs.Config)
			assert.Equal(t, tt.wantBuiltins, outputs.Config.BuiltinTools)
		})
	}
}

// Builtin bash executes in the runtime sandbox, so it must trigger the same
// pod isolation settings as skills; file-only builtin tools must not.
func Test_BuiltinTools_BashRequiresIsolation(t *testing.T) {
	scheme := schemev1.Scheme
	require.NoError(t, v1alpha2.AddToScheme(scheme))

	modelConfig := &v1alpha2.ModelConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-model", Namespace: "default"},
		Spec: v1alpha2.ModelConfigSpec{
			Model:    "gpt-4",
			Provider: v1alpha2.ModelProviderOpenAI,
		},
	}

	makeAgent := func(names ...v1alpha2.BuiltinToolName) *v1alpha2.Agent {
		return &v1alpha2.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
			Spec: v1alpha2.AgentSpec{
				Type: v1alpha2.AgentType_Declarative,
				Declarative: &v1alpha2.DeclarativeAgentSpec{
					SystemMessage: "You are a test agent",
					ModelConfig:   "test-model",
					Tools: []*v1alpha2.Tool{
						{
							Type:    v1alpha2.ToolProviderType_Builtin,
							Builtin: &v1alpha2.BuiltinTool{Names: names},
						},
					},
				},
			},
		}
	}

	tests := []struct {
		name           string
		agent          *v1alpha2.Agent
		wantPrivileged bool
	}{
		{
			name:           "bash triggers privileged sandbox",
			agent:          makeAgent(v1alpha2.BuiltinToolName_Bash),
			wantPrivileged: true,
		},
		{
			name:           "file tools alone do not",
			agent:          makeAgent(v1alpha2.BuiltinToolName_ReadFile, v1alpha2.BuiltinToolName_WriteFile),
			wantPrivileged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kubeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(modelConfig.DeepCopy()).
				Build()

			defaultModel := types.NamespacedName{Namespace: "default", Name: "test-model"}
			trans := translator.NewAdkApiTranslator(kubeClient, defaultModel, nil, "", nil)
			outputs, err := translator.TranslateAgent(context.Background(), trans, tt.agent)
			require.NoError(t, err)

			var deployment *appsv1.Deployment
			for _, obj := range outputs.Manifest {
				if dep, ok := obj.(*appsv1.Deployment); ok {
					deployment = dep
					break
				}
			}
			require.NotNil(t, deployment)

			securityContext := deployment.Spec.Template.Spec.Containers[0].SecurityContext
			if tt.wantPrivileged {
				require.NotNil(t, securityContext)
				require.NotNil(t, securityContext.Privileged)
				assert.True(t, *securityContext.Privileged)
			} else {
				privileged := securityContext != nil && securityContext.Privileged != nil && *securityContext.Privileged
				assert.False(t, privileged)
			}
		})
	}
}
