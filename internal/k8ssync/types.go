package k8ssync

// Only the fields the controller reads are decoded.

type objectMeta struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}

// Service is a core/v1 Service.
type Service struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		Type      string        `json:"type"`
		ClusterIP string        `json:"clusterIP"`
		Ports     []ServicePort `json:"ports"`
	} `json:"spec"`
}

// ServicePort is one port of a Service.
type ServicePort struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
}

// EndpointSlice is a discovery.k8s.io/v1 EndpointSlice.
type EndpointSlice struct {
	Metadata    objectMeta `json:"metadata"`
	AddressType string     `json:"addressType"` // IPv4, IPv6, FQDN
	Ports       []struct {
		Name     *string `json:"name"`
		Protocol *string `json:"protocol"`
		Port     *int    `json:"port"`
	} `json:"ports"`
	Endpoints []Endpoint `json:"endpoints"`
}

// Endpoint is one backend (usually a pod) of an EndpointSlice.
type Endpoint struct {
	Addresses  []string `json:"addresses"`
	Conditions struct {
		Ready       *bool `json:"ready"`
		Serving     *bool `json:"serving"`
		Terminating *bool `json:"terminating"`
	} `json:"conditions"`
	TargetRef *struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"targetRef"`
	NodeName *string `json:"nodeName"`
	Zone     *string `json:"zone"`
}

func (e Endpoint) ready() bool {
	// A missing ready condition means ready (API contract).
	return e.Conditions.Ready == nil || *e.Conditions.Ready
}

func (e Endpoint) terminating() bool {
	return e.Conditions.Terminating != nil && *e.Conditions.Terminating
}

func (e Endpoint) serving() bool {
	if e.Conditions.Serving != nil {
		return *e.Conditions.Serving
	}
	return e.ready()
}

func key(namespace, name string) string { return namespace + "/" + name }

// sliceService is the Service an EndpointSlice belongs to.
func sliceService(s EndpointSlice) string {
	return s.Metadata.Labels["kubernetes.io/service-name"]
}
