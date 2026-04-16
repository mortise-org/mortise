package ingress

type AppRef struct {
	Name      string
	Namespace string
}

type MiddlewareRef struct {
	Name      string
	Namespace string
}

type IngressProvider interface {
	ClassName() string
	Annotations(app AppRef, hostnames []string, middleware []MiddlewareRef) map[string]string
}
