package ingress

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

// Annotation keys emitted by the annotation-driven provider.
const (
	ExternalDNSHostnameAnnotation      = "external-dns.alpha.kubernetes.io/hostname"
	CertManagerClusterIssuerAnnotation = "cert-manager.io/cluster-issuer"
)

// AnnotationProviderConfig configures the annotation-driven IngressProvider.
type AnnotationProviderConfig struct {
	// ClassName is the Kubernetes ingress class to set on Ingresses. When
	// empty, no ingressClassName is set (cluster default is used).
	ClassName string

	// DefaultClusterIssuer is a static fallback. When Reader is set, the
	// provider reads PlatformConfig live and this field is only used if
	// PlatformConfig has no issuer configured.
	DefaultClusterIssuer string

	// Reader, if set, enables live PlatformConfig reads. The provider
	// reads spec.tls.certManagerClusterIssuer on each Annotations() call
	// so changes take effect without a pod restart. Uses the informer
	// cache, so reads are cheap.
	Reader client.Reader
}

type annotationProvider struct {
	cfg AnnotationProviderConfig
}

// NewAnnotationProvider returns an IngressProvider that emits standard
// annotations for ExternalDNS and cert-manager.
func NewAnnotationProvider(cfg AnnotationProviderConfig) IngressProvider {
	return &annotationProvider{cfg: cfg}
}

func (p *annotationProvider) ClassName() string {
	return p.cfg.ClassName
}

func (p *annotationProvider) Annotations(ctx context.Context, _ AppRef, hostnames []string, _ []MiddlewareRef) map[string]string {
	out := make(map[string]string, 2)

	if len(hostnames) > 0 {
		out[ExternalDNSHostnameAnnotation] = strings.Join(hostnames, ",")
	}

	issuer := p.resolveClusterIssuer(ctx)
	if issuer != "" {
		out[CertManagerClusterIssuerAnnotation] = issuer
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

// resolveClusterIssuer reads the cluster issuer from PlatformConfig (live)
// or falls back to the static default.
func (p *annotationProvider) resolveClusterIssuer(ctx context.Context) string {
	if p.cfg.Reader != nil {
		var pc mortisev1alpha1.PlatformConfig
		if err := p.cfg.Reader.Get(ctx, types.NamespacedName{Name: "platform"}, &pc); err == nil {
			if pc.Spec.TLS.CertManagerClusterIssuer != "" {
				return pc.Spec.TLS.CertManagerClusterIssuer
			}
		}
	}
	return p.cfg.DefaultClusterIssuer
}
