package ingress

import "strings"

// AnnotationProviderConfig configures the annotation-driven IngressProvider.
type AnnotationProviderConfig struct {
	// ClassName is the Kubernetes ingress class to set on Ingresses. When
	// empty, no ingressClassName is set (cluster default is used).
	ClassName string

	// DefaultClusterIssuer is the cert-manager ClusterIssuer written onto
	// every Ingress unless the environment opts out. When empty, no
	// cert-manager annotation is emitted.
	DefaultClusterIssuer string
}

// annotationProvider is the single "annotation-driven" implementation of
// IngressProvider. It emits standard annotations consumed by ExternalDNS and
// cert-manager — no vendor-specific CRDs.
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

func (p *annotationProvider) Annotations(_ AppRef, hostnames []string, _ []MiddlewareRef) map[string]string {
	out := make(map[string]string, 2)

	if len(hostnames) > 0 {
		out["external-dns.alpha.kubernetes.io/hostname"] = strings.Join(hostnames, ",")
	}

	if p.cfg.DefaultClusterIssuer != "" {
		out["cert-manager.io/cluster-issuer"] = p.cfg.DefaultClusterIssuer
	}

	if len(out) == 0 {
		return nil
	}
	return out
}
