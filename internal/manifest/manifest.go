// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/podmin-dev/podmin/internal/registry"
	"go.yaml.in/yaml/v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	serializerjson "k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	strictjson "sigs.k8s.io/json"
)

var namePattern = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,30}[a-z0-9])?$`)
var namespacePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

const (
	// IdentityVolumeName is the reserved workload identity volume name.
	IdentityVolumeName = "podmin-identity"
	// IdentityMountPath is the standard workload identity mount path.
	IdentityMountPath = "/var/run/secrets/podmin.dev/tls"
	// IdentityHostRoot is the Podmin-owned tmpfs root containing workload identity files.
	IdentityHostRoot = "/run/podmin"
	// InstallAnnotation identifies a workload produced by a built-in install command.
	InstallAnnotation = "podmin.dev/install"
)

// Deployment is one transformed Pod and its optional Service desired state.
type Deployment struct {
	Pod         []byte
	ServiceYAML []byte
	Service     *Service
}

// Service is the supported, provider-neutral Service desired state.
type Service struct {
	Name      string
	Namespace string
	Selector  map[string]string
	Ports     []ServicePort
}

// ServicePort is one supported Service port mapping.
type ServicePort struct {
	Name       string
	Protocol   string
	Port       int
	TargetPort int
}

// serviceDocument is Podmin's complete accepted Service wire format.
type serviceDocument struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace,omitempty"`
	} `json:"metadata"`
	Spec struct {
		Selector map[string]string     `json:"selector"`
		Ports    []servicePortDocument `json:"ports"`
	} `json:"spec"`
}

// servicePortDocument is Podmin's complete accepted Service port wire format.
type servicePortDocument struct {
	Name       string          `json:"name,omitempty"`
	Protocol   corev1.Protocol `json:"protocol,omitempty"`
	Port       int32           `json:"port"`
	TargetPort json.RawMessage `json:"targetPort,omitempty"`
}

// ValidID reports whether value is a valid cluster, NodeGroup, workload, or container identifier.
func ValidID(value string) bool { return namePattern.MatchString(value) }

// ValidNamespace reports whether value is a Kubernetes DNS-label namespace.
func ValidNamespace(value string) bool { return namespacePattern.MatchString(value) }

// encodeObject serializes a typed Kubernetes object as deterministic YAML.
func encodeObject(object runtime.Object) ([]byte, error) {
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	serializer := serializerjson.NewSerializerWithOptions(serializerjson.DefaultMetaFactory, scheme, scheme, serializerjson.SerializerOptions{Yaml: true, Pretty: true, Strict: true})
	return runtime.Encode(serializer, object)
}

// Init creates a minimal DaemonSet manifest and optional default Service targeting one NodeGroup.
func Init(name, nodeGroup, namespace string, images []string, serviceEnabled bool) ([]byte, error) {
	if !ValidID(name) || !ValidID(nodeGroup) || !ValidNamespace(namespace) {
		return nil, errors.New("invalid DaemonSet name, NodeGroup, or namespace")
	}
	if len(images) == 0 {
		return nil, errors.New("at least one --image is required")
	}
	if serviceEnabled && len(images) != 1 {
		return nil, errors.New("--service requires exactly one container image")
	}
	containers := make([]corev1.Container, 0, len(images))
	seen := map[string]bool{}
	for _, raw := range images {
		container, image := name, raw
		if strings.Contains(raw, "=") {
			container, image, _ = strings.Cut(raw, "=")
		}
		if !ValidID(container) || image == "" || seen[container] {
			return nil, fmt.Errorf("invalid --image %q", raw)
		}
		seen[container] = true
		value := corev1.Container{Name: container, Image: image, ImagePullPolicy: corev1.PullAlways}
		if serviceEnabled {
			value.Ports = []corev1.ContainerPort{{ContainerPort: 8080, Protocol: corev1.ProtocolTCP}}
			value.ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(8080)}}}
		}
		containers = append(containers, value)
	}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: corev1.PodSpec{NodeSelector: map[string]string{"podmin.dev/nodegroup": nodeGroup}, Containers: containers}}
	if err := transformPod(&pod, nil, ""); err != nil {
		return nil, err
	}
	labels := map[string]string{"app": name}
	daemonSet := &appsv1.DaemonSet{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "DaemonSet"}, ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: pod.Spec}}}
	daemonSetYAML, err := encodeObject(daemonSet)
	if err != nil || !serviceEnabled {
		return daemonSetYAML, err
	}
	serviceYAML := []byte(fmt.Sprintf("apiVersion: v1\nkind: Service\nmetadata:\n  name: %s\n  namespace: %s\nspec:\n  selector:\n    app: %s\n  ports:\n    - protocol: TCP\n      port: 8080\n      targetPort: 8080\n", name, namespace, name))
	return append(append(daemonSetYAML, []byte("---\n")...), serviceYAML...), nil
}

// ParsePod strictly decodes one typed Pod manifest.
func ParsePod(input []byte) (*corev1.Pod, error) {
	if err := validateYAML(input); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	var pod corev1.Pod
	if err := decodeStrict(input, &pod); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if pod.APIVersion != "v1" || pod.Kind != "Pod" {
		return nil, errors.New("apiVersion must be v1 and kind must be Pod")
	}
	return &pod, nil
}

// MarshalPod serializes a typed Pod as deterministic YAML.
func MarshalPod(pod *corev1.Pod) ([]byte, error) {
	pod.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}
	return encodeObject(pod)
}

// TransformPod strictly decodes and transforms one typed Pod.
func TransformPod(input []byte, images []string, revision string) (*corev1.Pod, error) {
	pod, err := ParsePod(input)
	if err != nil {
		return nil, err
	}
	if err := transformPod(pod, images, revision); err != nil {
		return nil, err
	}
	return pod, nil
}

// Transform strictly decodes, transforms, and serializes one typed Pod.
func Transform(input []byte, images []string, revision string) ([]byte, error) {
	pod, err := TransformPod(input, images, revision)
	if err != nil {
		return nil, err
	}
	return MarshalPod(pod)
}

// transformPod applies Podmin's image and identity policy to a typed Pod.
func transformPod(pod *corev1.Pod, images []string, revision string) error {
	if !ValidID(pod.Name) {
		return errors.New("metadata.name is invalid")
	}
	if len(pod.Spec.Containers) == 0 {
		return errors.New("spec.containers must be a non-empty sequence")
	}
	if revision != "" {
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations["podmin.dev/revision"] = revision
	}
	all := make([]*corev1.Container, 0, len(pod.Spec.InitContainers)+len(pod.Spec.Containers))
	byName := map[string]*corev1.Container{}
	for i := range pod.Spec.Containers {
		all = append(all, &pod.Spec.Containers[i])
	}
	for i := range pod.Spec.InitContainers {
		all = append(all, &pod.Spec.InitContainers[i])
	}
	for _, container := range all {
		if !ValidID(container.Name) {
			return errors.New("every container must have a valid name")
		}
		if byName[container.Name] != nil {
			return fmt.Errorf("duplicate container name %q", container.Name)
		}
		byName[container.Name] = container
	}
	if err := overrides(pod.Spec.Containers, byName, images); err != nil {
		return err
	}
	for _, container := range all {
		if container.Image == "" {
			return fmt.Errorf("container %q has no image", container.Name)
		}
		ref, err := registry.Parse(container.Image)
		if err != nil {
			return fmt.Errorf("container %q image %q: %w", container.Name, container.Image, err)
		}
		container.Image = ref.Name()
		if container.ImagePullPolicy == "" {
			container.ImagePullPolicy = corev1.PullAlways
		}
	}
	return injectIdentity(&pod.Spec, all, pod.Name)
}

// injectIdentity adds or validates the standard read-only workload identity mount.
func injectIdentity(spec *corev1.PodSpec, containers []*corev1.Container, pod string) error {
	hostPath := filepath.Join(IdentityHostRoot, pod, "identity")
	identity := -1
	for i := range spec.Volumes {
		volume := &spec.Volumes[i]
		if volume.Name == IdentityVolumeName {
			if identity >= 0 {
				return fmt.Errorf("volume %q may appear only once", IdentityVolumeName)
			}
			identity = i
			continue
		}
		if volume.HostPath != nil {
			clean := filepath.Clean(volume.HostPath.Path)
			if clean == IdentityHostRoot || strings.HasPrefix(clean, IdentityHostRoot+string(filepath.Separator)) {
				return fmt.Errorf("hostPath %s is reserved by Podmin", volume.HostPath.Path)
			}
		}
	}
	expectedSource := corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: hostPath, Type: hostPathType(corev1.HostPathDirectory)}}
	if identity < 0 {
		spec.Volumes = append(spec.Volumes, corev1.Volume{Name: IdentityVolumeName, VolumeSource: expectedSource})
	} else if !reflect.DeepEqual(spec.Volumes[identity].VolumeSource, expectedSource) {
		return fmt.Errorf("volume %q is reserved by Podmin", IdentityVolumeName)
	}
	expectedMount := corev1.VolumeMount{Name: IdentityVolumeName, MountPath: IdentityMountPath, ReadOnly: true}
	for _, container := range containers {
		found := false
		for _, mount := range container.VolumeMounts {
			path := filepath.Clean(mount.MountPath)
			overlaps := pathsOverlap(path, IdentityMountPath)
			reserved := mount.Name == IdentityVolumeName
			if !overlaps && !reserved {
				continue
			}
			if found || !reflect.DeepEqual(mount, expectedMount) {
				return fmt.Errorf("mount path %s is reserved by Podmin", IdentityMountPath)
			}
			found = true
		}
		if !found {
			container.VolumeMounts = append(container.VolumeMounts, expectedMount)
		}
	}
	return nil
}

// hostPathType returns a pointer to a host path type.
func hostPathType(value corev1.HostPathType) *corev1.HostPathType { return &value }

// pathsOverlap reports whether either path contains the other.
func pathsOverlap(left, right string) bool {
	contains := func(parent, child string) bool {
		relative, err := filepath.Rel(parent, child)
		return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
	}
	return contains(left, right) || contains(right, left)
}

// ParseDeployment extracts exactly one DaemonSet and at most one constrained Service.
func ParseDeployment(input []byte, images []string, revision, expectedName, nodeGroup string) (Deployment, error) {
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(input)))
	var daemonSet *appsv1.DaemonSet
	var service *serviceDocument
	for {
		document, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Deployment{}, fmt.Errorf("parse manifest stream: %w", err)
		}
		if len(bytes.TrimSpace(document)) == 0 {
			continue
		}
		if err := validateYAML(document); err != nil {
			return Deployment{}, fmt.Errorf("parse manifest stream: %w", err)
		}
		var meta metav1.TypeMeta
		if err := utilyaml.Unmarshal(document, &meta); err != nil {
			return Deployment{}, fmt.Errorf("parse manifest stream: %w", err)
		}
		switch schema.FromAPIVersionAndKind(meta.APIVersion, meta.Kind) {
		case appsv1.SchemeGroupVersion.WithKind("DaemonSet"):
			if daemonSet != nil {
				return Deployment{}, errors.New("manifest stream must contain exactly one apps/v1 DaemonSet")
			}
			daemonSet = &appsv1.DaemonSet{}
			if err := decodeStrict(document, daemonSet); err != nil {
				return Deployment{}, fmt.Errorf("parse DaemonSet: %w", err)
			}
			if err := rejectSchedulingFields(document); err != nil {
				return Deployment{}, err
			}
		case corev1.SchemeGroupVersion.WithKind("Service"):
			if service != nil {
				return Deployment{}, errors.New("manifest stream may contain at most one Service")
			}
			service = &serviceDocument{}
			if err := decodeService(document, service); err != nil {
				return Deployment{}, fmt.Errorf("parse Service: %w", err)
			}
		default:
			return Deployment{}, fmt.Errorf("unsupported kind %q", meta.Kind)
		}
	}
	if daemonSet == nil {
		return Deployment{}, errors.New("manifest stream must contain exactly one apps/v1 DaemonSet")
	}
	name, namespace := daemonSet.Name, daemonSet.Namespace
	if namespace == "" {
		namespace = "default"
	}
	if expectedName == "" {
		expectedName = name
	}
	if name != expectedName || !ValidID(name) || !ValidNamespace(namespace) {
		return Deployment{}, errors.New("DaemonSet metadata name or namespace is invalid")
	}
	spec := daemonSet.Spec.Template.Spec
	if nodeGroup == "" {
		nodeGroup = spec.NodeSelector["podmin.dev/nodegroup"]
	}
	if !ValidID(nodeGroup) || len(spec.NodeSelector) != 1 || spec.NodeSelector["podmin.dev/nodegroup"] != nodeGroup {
		return Deployment{}, errors.New("DaemonSet Pod template nodegroup selector does not match --nodegroup")
	}
	if spec.NodeName != "" || spec.Affinity != nil || spec.SchedulerName != "" || len(spec.TopologySpreadConstraints) != 0 {
		return Deployment{}, errors.New("DaemonSet Pod template contains an unsupported scheduling field")
	}
	spec.NodeSelector = nil
	pod := &corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: *daemonSet.Spec.Template.ObjectMeta.DeepCopy(), Spec: spec}
	pod.Name, pod.Namespace = name, namespace
	if err := transformPod(pod, images, revision); err != nil {
		return Deployment{}, err
	}
	result := Deployment{}
	if service != nil {
		parsed, canonical, err := parseService(service)
		if err != nil {
			return Deployment{}, err
		}
		if parsed.Namespace != namespace {
			return Deployment{}, fmt.Errorf("Service namespace %q does not equal DaemonSet namespace %q", parsed.Namespace, namespace)
		}
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations["podmin.dev/service"] = parsed.Name
		result.Service, result.ServiceYAML = &parsed, canonical
	}
	var err error
	result.Pod, err = encodeObject(pod)
	return result, err
}

// rejectSchedulingFields rejects forbidden scheduling keys even when their typed values are empty.
func rejectSchedulingFields(document []byte) error {
	jsonDocument, err := utilyaml.ToJSON(document)
	if err != nil {
		return fmt.Errorf("parse DaemonSet scheduling fields: %w", err)
	}
	var presence struct {
		Spec struct {
			Template struct {
				Spec map[string]json.RawMessage `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(jsonDocument, &presence); err != nil {
		return fmt.Errorf("parse DaemonSet scheduling fields: %w", err)
	}
	for _, field := range []string{"nodeName", "affinity", "schedulerName", "topologySpreadConstraints"} {
		if _, exists := presence.Spec.Template.Spec[field]; exists {
			return fmt.Errorf("DaemonSet Pod template scheduling field %q is not supported", field)
		}
	}
	return nil
}

// ParseService validates one standalone Service document for agent consumption.
func ParseService(input []byte) (Service, error) {
	if err := validateYAML(input); err != nil {
		return Service{}, fmt.Errorf("parse Service: %w", err)
	}
	var service serviceDocument
	if err := decodeService(input, &service); err != nil {
		return Service{}, fmt.Errorf("parse Service: %w", err)
	}
	if service.APIVersion != "v1" || service.Kind != "Service" {
		return Service{}, errors.New("manifest must contain one apiVersion v1 kind Service mapping")
	}
	result, _, err := parseService(&service)
	return result, err
}

// decodeService strictly decodes Podmin's complete Service wire format.
func decodeService(input []byte, service *serviceDocument) error {
	return decodeStrict(input, service)
}

// decodeStrict converts one YAML document and strictly decodes its exact JSON field names.
func decodeStrict(input []byte, result any) error {
	jsonDocument, err := utilyaml.ToJSON(input)
	if err != nil {
		return err
	}
	strictErrors, err := strictjson.UnmarshalStrict(jsonDocument, result, strictjson.DisallowDuplicateFields, strictjson.DisallowUnknownFields)
	if err != nil {
		return err
	}
	return errors.Join(strictErrors...)
}

// validateYAML rejects syntax that cannot map deterministically to JSON objects.
func validateYAML(input []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(input))
	documents := 0
	for {
		var document yaml.Node
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if len(document.Content) == 0 {
			continue
		}
		documents++
		if err := validateYAMLNode(&document, map[*yaml.Node]bool{}); err != nil {
			return err
		}
	}
	if documents != 1 {
		return errors.New("manifest must contain exactly one YAML document")
	}
	return nil
}

// validateYAMLNode recursively validates aliases and rejects merges, non-string keys, and duplicate keys.
func validateYAMLNode(node *yaml.Node, visiting map[*yaml.Node]bool) error {
	if node.Kind == yaml.AliasNode {
		if node.Alias == nil || visiting[node.Alias] {
			return errors.New("invalid recursive YAML alias")
		}
		visiting[node.Alias] = true
		err := validateYAMLNode(node.Alias, visiting)
		delete(visiting, node.Alias)
		return err
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "<<" {
				return errors.New("YAML mapping keys must be unique strings without merges")
			}
			if seen[key.Value] {
				return fmt.Errorf("duplicate YAML mapping key %q", key.Value)
			}
			seen[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if err := validateYAMLNode(child, visiting); err != nil {
			return err
		}
	}
	return nil
}

// parseService validates and canonicalizes the deliberately small Service subset.
func parseService(document *serviceDocument) (Service, []byte, error) {
	if document.APIVersion != "v1" || document.Kind != "Service" {
		return Service{}, nil, errors.New("manifest must contain one apiVersion v1 kind Service mapping")
	}
	if document.Metadata.Namespace == "" {
		document.Metadata.Namespace = "default"
	}
	if !ValidID(document.Metadata.Name) || !ValidNamespace(document.Metadata.Namespace) {
		return Service{}, nil, errors.New("Service metadata.name is invalid")
	}
	if len(document.Spec.Selector) == 0 || len(document.Spec.Ports) == 0 {
		return Service{}, nil, errors.New("Service selector and ports must be non-empty")
	}
	result := Service{Name: document.Metadata.Name, Namespace: document.Metadata.Namespace, Selector: document.Spec.Selector}
	canonicalPorts := make([]map[string]any, 0, len(document.Spec.Ports))
	seen := map[string]bool{}
	for _, wirePort := range document.Spec.Ports {
		port := corev1.ServicePort{Name: wirePort.Name, Protocol: wirePort.Protocol, Port: wirePort.Port}
		if port.Protocol == "" {
			port.Protocol = corev1.ProtocolTCP
		}
		if port.Protocol != corev1.ProtocolTCP && port.Protocol != corev1.ProtocolUDP {
			return Service{}, nil, fmt.Errorf("unsupported Service protocol %q", port.Protocol)
		}
		if port.Port < 1 || port.Port > 65535 {
			return Service{}, nil, errors.New("Service port must be an integer from 1 through 65535")
		}
		if len(wirePort.TargetPort) == 0 {
			port.TargetPort = intstr.FromInt32(port.Port)
		} else if err := json.Unmarshal(wirePort.TargetPort, &port.TargetPort.IntVal); err != nil {
			return Service{}, nil, errors.New("Service targetPort must be an integer from 1 through 65535")
		}
		if port.TargetPort.IntVal < 1 || port.TargetPort.IntVal > 65535 {
			return Service{}, nil, errors.New("Service targetPort must be an integer from 1 through 65535")
		}
		identity := fmt.Sprintf("%s/%d", port.Protocol, port.Port)
		if seen[identity] {
			return Service{}, nil, fmt.Errorf("duplicate Service port %s", identity)
		}
		seen[identity] = true
		result.Ports = append(result.Ports, ServicePort{Name: port.Name, Protocol: string(port.Protocol), Port: int(port.Port), TargetPort: int(port.TargetPort.IntVal)})
		canonicalPort := map[string]any{"protocol": port.Protocol, "port": port.Port, "targetPort": port.TargetPort.IntVal}
		if port.Name != "" {
			canonicalPort["name"] = port.Name
		}
		canonicalPorts = append(canonicalPorts, canonicalPort)
	}
	canonical, err := yaml.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata":   map[string]string{"name": result.Name, "namespace": result.Namespace},
		"spec":       map[string]any{"selector": result.Selector, "ports": canonicalPorts},
	})
	return result, canonical, err
}

// Name returns metadata.name from transformed YAML.
func Name(input []byte) (string, error) {
	if err := validateYAML(input); err != nil {
		return "", err
	}
	var pod corev1.Pod
	if err := decodeStrict(input, &pod); err != nil {
		return "", err
	}
	if pod.Name == "" {
		return "", errors.New("empty manifest")
	}
	return pod.Name, nil
}

// overrides applies the exact named or bare override rules.
func overrides(regular []corev1.Container, all map[string]*corev1.Container, images []string) error {
	bare := ""
	named := map[string]string{}
	for _, raw := range images {
		if strings.Contains(raw, "=") {
			name, image, _ := strings.Cut(raw, "=")
			if name == "" || image == "" || named[name] != "" {
				return fmt.Errorf("invalid or duplicate --image %q", raw)
			}
			named[name] = image
		} else if bare != "" || raw == "" {
			return errors.New("only one bare --image is allowed")
		} else {
			bare = raw
		}
	}
	if bare != "" && len(named) != 0 {
		return errors.New("bare and named --image values cannot be combined")
	}
	if bare != "" {
		if len(regular) != 1 {
			return errors.New("bare --image requires exactly one regular container")
		}
		all[regular[0].Name].Image = bare
	}
	for name, image := range named {
		container := all[name]
		if container == nil {
			return fmt.Errorf("unknown container %q", name)
		}
		container.Image = image
	}
	return nil
}
