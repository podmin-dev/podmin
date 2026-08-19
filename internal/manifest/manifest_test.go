// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// TestIndexRoundTripDeterminismAndDigest verifies canonical indexes and payload integrity.
func TestIndexRoundTripDeterminismAndDigest(t *testing.T) {
	payload := []byte("pod")
	object := IndexObject("nodegroups/group/pods/sha512/" + Digest(payload) + ".yaml")
	index := Index{
		"group/z": {Pod: object},
		"group/a": {Pod: object},
	}
	one, err := MarshalIndex(index)
	if err != nil {
		t.Fatal(err)
	}
	two, err := MarshalIndex(index)
	if err != nil || !bytes.Equal(one, two) {
		t.Fatalf("non-deterministic index: %v", err)
	}
	parsed, err := ParseIndex(one)
	if err != nil {
		t.Fatal(err)
	}
	if err = parsed["group/a"].Pod.Verify(payload); err != nil {
		t.Fatal(err)
	}
	if err = parsed["group/a"].Pod.Verify([]byte("corrupt")); err == nil {
		t.Fatal("corrupt payload verified")
	}
}

// TestParseIndexRejectsDuplicateKeysAndInvalidObjects verifies recursive JSON uniqueness and content addressing.
func TestParseIndexRejectsDuplicateKeysAndInvalidObjects(t *testing.T) {
	digest := Digest([]byte("pod"))
	for _, body := range []string{
		`{"group/app":{"pod":"one","pod":"two"}}`,
		`{"group/app":{"pod":"nodegroups/other/pods/sha512/` + digest + `.yaml"}}`,
		`{"group/app":{"pod":"nodegroups/group/pods/sha512/short.yaml"}}`,
	} {
		if _, err := ParseIndex([]byte(body)); err == nil {
			t.Fatalf("unexpectedly accepted malformed index: %s", body)
		}
	}
}

// TestTransformPreservesAndOverrides verifies typed field preservation and named overrides.
func TestTransformPreservesAndOverrides(t *testing.T) {
	in := []byte("apiVersion: v1\nkind: Pod\nmetadata:\n  name: app\nspec:\n  containers:\n    - name: web\n      command: [serve]\n    - name: sidecar\n      image: old\n")
	out, err := Transform(in, []string{"web=registry.podmin.internal/apps/example/web:new", "sidecar=registry.podmin.internal/apps/example/sidecar:newer"}, "revision")
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, value := range []string{"command:", "- serve", "image: registry.podmin.internal/apps/example/web:new", "image: registry.podmin.internal/apps/example/sidecar:newer", "imagePullPolicy: Always", "podmin.dev/revision: revision"} {
		if !strings.Contains(text, value) {
			t.Errorf("output lacks %q:\n%s", value, text)
		}
	}
}

// TestTypedParsersRejectUnknownAndUnsupportedServiceFields verifies strict typed boundaries.
func TestTypedParsersRejectUnknownAndUnsupportedServiceFields(t *testing.T) {
	pod := []byte("apiVersion: v1\nkind: Pod\nmetadata: {name: app, mystery: true}\nspec: {containers: [{name: app, image: registry.podmin.internal/apps/example/app:latest}]}\n")
	if _, err := Transform(pod, nil, ""); err == nil {
		t.Fatal("Transform accepted an unknown typed field")
	}
	caseVariantPod := []byte("apiVersion: v1\nkind: Pod\nMetadata: {name: app}\nspec: {containers: [{name: app, image: registry.podmin.internal/apps/example/app:latest}]}\n")
	if _, err := Transform(caseVariantPod, nil, ""); err == nil {
		t.Fatal("Transform accepted a case-variant typed field")
	}
	services := [][]byte{
		[]byte("apiVersion: v1\nkind: Service\nmetadata: {name: app}\nspec: {selector: {app: app}, ports: [{port: 80}], type: NodePort}\n"),
		[]byte("apiVersion: v1\nkind: Service\nmetadata: {name: app}\nspec: {selector: {app: app}, ports: [{port: 80}], type: \"\"}\n"),
		[]byte("apiVersion: v1\nkind: Service\nmetadata: {name: app, labels: {app: app}}\nspec: {selector: {app: app}, ports: [{port: 80}]}\n"),
		[]byte("apiVersion: v1\nkind: Service\nmetadata: {name: app}\nspec: {selector: {app: app}, ports: [{port: 80, nodePort: 30080}]}\n"),
		[]byte("apiVersion: v1\nkind: Service\nmetadata: {name: app}\nspec: {selector: {app: app}, ports: [{port: 80, Port: 81}]}\n"),
		[]byte("apiVersion: v1\nkind: Service\nmetadata: {name: app}\nspec: {selector: {app: app}, ports: [{port: 80, nodePort: 0}]}\n"),
		[]byte("apiVersion: v1\nkind: Service\nmetadata: {name: app}\nspec: {selector: {app: app}, ports: [{port: 80, targetPort: 0}]}\n"),
		[]byte("apiVersion: v1\nkind: Service\nmetadata: {name: app}\nspec: {selector: {app: app}, ports: [{port: 80, targetPort: null}]}\n"),
		[]byte("apiVersion: v1\nkind: Service\nmetadata: {name: app}\nspec: {selector: {app: true}, ports: [{port: 80}]}\n"),
		[]byte("apiVersion: v1\nkind: Service\nmetadata: {name: app}\nspec: {selector: {app: app}, ports: [{port: 80}]}\nstatus: {}\n"),
		[]byte("apiVersion: v1\nkind: Service\nmetadata: {name: app}\nspec: {selector: {app: app}, ports: [{port: 80}], mystery: true}\n"),
	}
	for _, service := range services {
		if _, err := ParseService(service); err == nil {
			t.Fatalf("ParseService accepted an unsupported field:\n%s", service)
		}
	}
}

// TestTransformImageRules verifies exact repeated image failures.
func TestTransformImageRules(t *testing.T) {
	in := []byte("apiVersion: v1\nkind: Pod\nmetadata: {name: app}\nspec:\n  containers:\n    - {name: one, image: old}\n    - {name: two, image: old}\n")
	for _, images := range [][]string{{"bare"}, {"one=a", "one=b"}, {"missing=a"}, {"bare", "one=a"}} {
		if _, err := Transform(in, images, ""); err == nil {
			t.Errorf("images %v unexpectedly succeeded", images)
		}
	}
}

// TestTransformNormalizesClusterImageShorthand verifies push destinations work as image inputs.
func TestTransformNormalizesClusterImageShorthand(t *testing.T) {
	in := []byte("apiVersion: v1\nkind: Pod\nmetadata: {name: hello}\nspec: {containers: [{name: hello, image: hello}]}\n")
	out, err := Transform(in, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "image: registry.podmin.internal/apps/hello:latest") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

// TestYAMLParsersRejectRecursiveDuplicateKeys verifies duplicate mappings are rejected before transformation.
func TestYAMLParsersRejectRecursiveDuplicateKeys(t *testing.T) {
	pod := []byte("apiVersion: v1\nkind: Pod\nmetadata: {name: app}\nspec: {containers: [{name: app, image: registry.podmin.internal/apps/example/app:latest, image: registry.podmin.internal/apps/example/other:latest}]}\n")
	service := []byte("apiVersion: v1\nkind: Service\nmetadata: {name: app}\nspec: {selector: {app: one, app: two}, ports: [{port: 80}]}\n")
	daemonSet := []byte("apiVersion: apps/v1\nkind: DaemonSet\nmetadata: {name: app}\nspec: {template: {metadata: {}, spec: {nodeSelector: {podmin.dev/nodegroup: workers}, containers: [{name: app, image: registry.podmin.internal/apps/example/app:latest, image: duplicate}]}}}\n")
	if _, err := Transform(pod, nil, "revision"); err == nil {
		t.Fatal("Transform accepted duplicate nested key")
	}
	if _, err := ParseService(service); err == nil {
		t.Fatal("ParseService accepted duplicate nested key")
	}
	if _, err := ParseDeployment(daemonSet, nil, "", "app", "workers"); err == nil {
		t.Fatal("ParseDeployment accepted duplicate nested key")
	}
	merged := []byte("apiVersion: apps/v1\nkind: DaemonSet\nmetadata: {name: app}\nspec:\n  template:\n    metadata: {}\n    spec:\n      nodeSelector:\n        <<: &selector {podmin.dev/nodegroup: other}\n        podmin.dev/nodegroup: workers\n      containers: [{name: app, image: registry.podmin.internal/apps/example/app:latest}]\n")
	if _, err := ParseDeployment(merged, nil, "", "app", "workers"); err == nil {
		t.Fatal("ParseDeployment accepted a YAML merge key")
	}
	mergeOnly := []byte("apiVersion: apps/v1\nkind: DaemonSet\nmetadata: {name: app}\nspec:\n  template:\n    metadata: {}\n    spec:\n      nodeSelector:\n        <<: {podmin.dev/nodegroup: workers}\n      containers: [{name: app, image: registry.podmin.internal/apps/example/app:latest}]\n")
	if _, err := ParseDeployment(mergeOnly, nil, "", "app", "workers"); err == nil {
		t.Fatal("ParseDeployment accepted a non-colliding YAML merge key")
	}
	mixedKeys := []byte("apiVersion: v1\nkind: Pod\nmetadata: {name: app, labels: {1: first, \"1\": second}}\nspec: {containers: [{name: app, image: registry.podmin.internal/apps/example/app:latest}]}\n")
	if _, err := Transform(mixedKeys, nil, ""); err == nil {
		t.Fatal("Transform accepted non-string YAML mapping keys")
	}
}

// TestParseDeploymentValidatesDerivedNodeGroup verifies validate-mode selector IDs are constrained.
func TestParseDeploymentValidatesDerivedNodeGroup(t *testing.T) {
	input := []byte("apiVersion: apps/v1\nkind: DaemonSet\nmetadata: {name: app}\nspec: {template: {metadata: {}, spec: {nodeSelector: {podmin.dev/nodegroup: INVALID}, containers: [{name: app, image: registry.podmin.internal/apps/example/app:latest}]}}}\n")
	if _, err := ParseDeployment(input, nil, "", "app", ""); err == nil {
		t.Fatal("accepted invalid derived NodeGroup")
	}
}

// TestInitNamedImages verifies repeated named initialization.
func TestInitNamedImages(t *testing.T) {
	out, err := Init(InitConfig{Name: "app", NodeGroup: "workers", Namespace: "product", Images: []string{"web=registry.podmin.internal/apps/example/web:latest", "sidecar=registry.podmin.internal/apps/example/sidecar:latest"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "kind: DaemonSet") || !strings.Contains(string(out), "namespace: product") || !strings.Contains(string(out), "podmin.dev/nodegroup: workers") || !strings.Contains(string(out), "name: sidecar") || !strings.Contains(string(out), IdentityMountPath) {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

// TestInitServiceGeneratesTheOpinionatedPort verifies the built-in Service and readiness contract.
func TestInitServiceGeneratesTheOpinionatedPort(t *testing.T) {
	out, err := Init(InitConfig{Name: "hello", NodeGroup: "default", Namespace: "default", Images: []string{"hello:v1"}, Service: true})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := ParseDeployment(out, nil, "", "hello", "default")
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Service == nil || deployment.Service.Name != "hello" || len(deployment.Service.Ports) != 1 || deployment.Service.Ports[0].Port != 443 || deployment.Service.Ports[0].TargetPort != 8443 {
		t.Fatalf("generated Service = %#v", deployment.Service)
	}
	if bytes.Contains(deployment.ServiceYAML, []byte("status:")) {
		t.Fatalf("generated Service contains unsupported status:\n%s", deployment.ServiceYAML)
	}
	if _, err = ParseService(deployment.ServiceYAML); err != nil {
		t.Fatalf("generated Service is not accepted by the agent: %v\n%s", err, deployment.ServiceYAML)
	}
	pod, err := ParsePod(deployment.Pod)
	if err != nil {
		t.Fatal(err)
	}
	if ports := pod.Spec.Containers[0].Ports; len(ports) != 1 || ports[0].ContainerPort != 8443 || ports[0].Protocol != corev1.ProtocolTCP {
		t.Fatalf("generated container ports = %#v", ports)
	}
	wantEnv := []corev1.EnvVar{
		{Name: "BRAND_NAME", Value: "Podmin"},
		{Name: "BRAND_URL", Value: "https://podmin.dev"},
		{Name: "BRAND_LOGO", Value: ""},
		{Name: "PORT", Value: "8443"},
		{Name: "TLS_CERT_FILE", Value: IdentityMountPath + "/tls.crt"},
		{Name: "TLS_KEY_FILE", Value: IdentityMountPath + "/tls.key"},
	}
	if env := pod.Spec.Containers[0].Env; !reflect.DeepEqual(env, wantEnv) {
		t.Fatalf("generated container environment = %#v, want %#v", env, wantEnv)
	}
	other, err := Init(InitConfig{Name: "app", NodeGroup: "default", Namespace: "default", Images: []string{"registry.podmin.internal/apps/other:v1"}, Service: true})
	if err != nil {
		t.Fatal(err)
	}
	otherDeployment, err := ParseDeployment(other, nil, "", "app", "default")
	if err != nil {
		t.Fatal(err)
	}
	otherPod, err := ParsePod(otherDeployment.Pod)
	if err != nil {
		t.Fatal(err)
	}
	wantTLS := wantEnv[4:]
	if env := otherPod.Spec.Containers[0].Env; !reflect.DeepEqual(env, wantTLS) {
		t.Fatalf("generated non-Hello environment = %#v, want %#v", env, wantTLS)
	}
	probe := pod.Spec.Containers[0].ReadinessProbe
	if probe == nil || probe.TCPSocket == nil || probe.TCPSocket.Port.IntVal != 8443 {
		t.Fatalf("generated readiness probe = %#v", probe)
	}
	if _, err = Init(InitConfig{Name: "hello", NodeGroup: "default", Namespace: "default", Images: []string{"app=hello", "sidecar=sidecar"}, Service: true}); err == nil {
		t.Fatal("generated a default Service for multiple containers")
	}
}

// TestInitServiceGeneratesMultiplePorts verifies custom Service mappings and primary readiness.
func TestInitServiceGeneratesMultiplePorts(t *testing.T) {
	out, err := Init(InitConfig{Name: "api", NodeGroup: "default", Namespace: "default", Images: []string{"api:v1"}, Service: true, Ports: []ServicePort{{Port: 443, TargetPort: 8443}, {Port: 8082, TargetPort: 8082}}})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := ParseDeployment(out, nil, "", "api", "default")
	if err != nil {
		t.Fatal(err)
	}
	want := []ServicePort{{Name: "tcp-443", Protocol: "TCP", Port: 443, TargetPort: 8443}, {Name: "tcp-8082", Protocol: "TCP", Port: 8082, TargetPort: 8082}}
	if deployment.Service == nil || !reflect.DeepEqual(deployment.Service.Ports, want) {
		t.Fatalf("generated Service ports = %#v, want %#v", deployment.Service, want)
	}
	pod, err := ParsePod(deployment.Pod)
	if err != nil {
		t.Fatal(err)
	}
	if ports := pod.Spec.Containers[0].Ports; len(ports) != 2 || ports[0].ContainerPort != 8443 || ports[1].ContainerPort != 8082 {
		t.Fatalf("generated container ports = %#v", ports)
	}
	if probe := pod.Spec.Containers[0].ReadinessProbe; probe == nil || probe.TCPSocket == nil || probe.TCPSocket.Port.IntVal != 8443 {
		t.Fatalf("generated readiness probe = %#v", probe)
	}
	if _, err = Init(InitConfig{Name: "api", NodeGroup: "default", Namespace: "default", Images: []string{"api:v1"}, Service: true, Ports: []ServicePort{{Port: 443, TargetPort: 8443}, {Port: 443, TargetPort: 9443}}}); err == nil {
		t.Fatal("accepted duplicate Service ports")
	}
}

// TestInitAddsEnvironmentAndSecretAnnotation verifies built-in deploy configuration.
func TestInitAddsEnvironmentAndSecretAnnotation(t *testing.T) {
	out, err := Init(InitConfig{Name: "app", NodeGroup: "default", Namespace: "default", Images: []string{"app:v1"}, Env: map[string]string{"LOG_LEVEL": "debug"}, SecretKeys: []string{"database-password"}, SecretsProvider: "aws-secrets-manager"})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := ParseDeployment(out, nil, "", "app", "default")
	if err != nil {
		t.Fatal(err)
	}
	pod, err := ParsePod(deployment.Pod)
	if err != nil {
		t.Fatal(err)
	}
	if env := pod.Spec.Containers[0].Env; len(env) != 1 || env[0].Name != "LOG_LEVEL" || env[0].Value != "debug" {
		t.Fatalf("environment = %#v", env)
	}
	if annotation := pod.Annotations["podmin.dev/aws-secrets-manager"]; annotation != "database-password" {
		t.Fatalf("secret annotation = %q", annotation)
	}
	if _, err = Init(InitConfig{Name: "app", NodeGroup: "default", Namespace: "default", Images: []string{"app:v1"}, Service: true, Env: map[string]string{"TLS_CERT_FILE": "other"}}); err == nil {
		t.Fatal("accepted environment conflicting with automatic TLS configuration")
	}
}

// TestTransformIdentityMountIsIdempotentAndReserved verifies standard mounts and collision rejection.
func TestTransformIdentityMountIsIdempotentAndReserved(t *testing.T) {
	input := []byte("apiVersion: v1\nkind: Pod\nmetadata: {name: app}\nspec:\n  initContainers: [{name: init, image: registry.podmin.internal/apps/example/init:latest}]\n  containers: [{name: app, image: registry.podmin.internal/apps/example/app:latest}]\n")
	one, err := Transform(input, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	two, err := Transform(one, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one, two) || strings.Count(string(one), "mountPath: "+IdentityMountPath) != 2 || !strings.Contains(string(one), "path: /run/podmin/app/identity") {
		t.Fatalf("identity mount is not complete and idempotent:\n%s", one)
	}
	collision := []byte("apiVersion: v1\nkind: Pod\nmetadata: {name: app}\nspec:\n  volumes: [{name: podmin-identity, emptyDir: {}}]\n  containers: [{name: app, image: registry.podmin.internal/apps/example/app:latest}]\n")
	if _, err = Transform(collision, nil, ""); err == nil {
		t.Fatal("accepted reserved identity volume collision")
	}
	aliases := [][]byte{
		[]byte("apiVersion: v1\nkind: Pod\nmetadata: {name: app}\nspec:\n  volumes: [{name: alias, hostPath: {path: /run/podmin/app}}]\n  containers: [{name: app, image: registry.podmin.internal/apps/example/app:latest}]\n"),
		[]byte("apiVersion: v1\nkind: Pod\nmetadata: {name: app}\nspec:\n  volumes: [{name: alias, hostPath: {path: /run/podmin/}}]\n  containers: [{name: app, image: registry.podmin.internal/apps/example/app:latest}]\n"),
		[]byte("apiVersion: v1\nkind: Pod\nmetadata: {name: app}\nspec:\n  volumes: [{name: podmin-identity, hostPath: {path: /run/podmin/app/identity, type: Directory}}]\n  containers: [{name: app, image: registry.podmin.internal/apps/example/app:latest, volumeMounts: [{name: podmin-identity, mountPath: /tmp/identity}]}]\n"),
		[]byte("apiVersion: v1\nkind: Pod\nmetadata: {name: app}\nspec:\n  volumes: [{name: podmin-identity, hostPath: {path: /run/podmin/app/identity, type: Directory}}]\n  containers: [{name: app, image: registry.podmin.internal/apps/example/app:latest, volumeMounts: [{name: podmin-identity, mountPath: /var/run/secrets/podmin.dev/tls, readOnly: true, subPath: tls}]}]\n"),
		[]byte("apiVersion: v1\nkind: Pod\nmetadata: {name: app}\nspec:\n  volumes: [{name: root, emptyDir: {}}]\n  containers: [{name: app, image: registry.podmin.internal/apps/example/app:latest, volumeMounts: [{name: root, mountPath: /}]}]\n"),
		[]byte("apiVersion: v1\nkind: Pod\nmetadata: {name: app}\nspec:\n  volumes: [{name: podmin-identity, hostPath: {path: /run/podmin/app/identity, type: Directory}}]\n  containers: [{name: app, image: registry.podmin.internal/apps/example/app:latest, volumeMounts: [{name: podmin-identity, mountPath: /var/run/secrets/podmin.dev/tls, readOnly: true, mountPropagation: Bidirectional}]}]\n"),
	}
	for _, alias := range aliases {
		if _, err = Transform(alias, nil, ""); err == nil {
			t.Fatalf("accepted writable or partial identity alias:\n%s", alias)
		}
	}
}

// TestParseDeploymentService verifies stream order, defaults, and typed output.
func TestParseDeploymentService(t *testing.T) {
	input := []byte("apiVersion: v1\nkind: Service\nmetadata: {name: frontend}\nspec:\n  selector: {app: app}\n  ports: [{port: 80}]\n---\napiVersion: apps/v1\nkind: DaemonSet\nmetadata: {name: app}\nspec:\n  template:\n    metadata: {labels: {app: app}, annotations: {example: retained}}\n    spec:\n      nodeSelector: {podmin.dev/nodegroup: workers}\n      containers: [{name: app, image: registry.podmin.internal/apps/example/app:latest}]\n")
	deployment, err := ParseDeployment(input, nil, "revision", "app", "workers")
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Service == nil || deployment.Service.Ports[0].Protocol != "TCP" || deployment.Service.Ports[0].TargetPort != 80 {
		t.Fatalf("unexpected Service: %#v", deployment.Service)
	}
	if !strings.Contains(string(deployment.ServiceYAML), "targetPort: 80") {
		t.Fatalf("Service is not canonicalized:\n%s", deployment.ServiceYAML)
	}
	pod := string(deployment.Pod)
	for _, expected := range []string{"kind: Pod", "namespace: default", "example: retained", "podmin.dev/service: frontend", "podmin.dev/revision: revision"} {
		if !strings.Contains(pod, expected) {
			t.Errorf("extracted Pod lacks %q:\n%s", expected, pod)
		}
	}
	if strings.Contains(pod, "podmin.dev/nodegroup") {
		t.Fatalf("extracted Pod retained nodegroup selector:\n%s", pod)
	}
}

// TestParseDeploymentRejectsAdditionalScheduling verifies NodeGroup is the sole scheduling target.
func TestParseDeploymentRejectsAdditionalScheduling(t *testing.T) {
	fields := []string{
		"nodeSelector: {podmin.dev/nodegroup: workers, disk: fast}",
		"nodeSelector: {podmin.dev/nodegroup: workers}\n      nodeName: worker-1",
		"nodeSelector: {podmin.dev/nodegroup: workers}\n      NodeName: \"\"",
		"nodeSelector: {podmin.dev/nodegroup: workers}\n      affinity: {nodeAffinity: {}}",
		"nodeSelector: {podmin.dev/nodegroup: workers}\n      schedulerName: custom",
		"nodeSelector: {podmin.dev/nodegroup: workers}\n      topologySpreadConstraints: []",
	}
	for _, scheduling := range fields {
		input := []byte("apiVersion: apps/v1\nkind: DaemonSet\nmetadata: {name: app}\nspec:\n  template:\n    metadata: {}\n    spec:\n      " + scheduling + "\n      containers: [{name: app, image: registry.podmin.internal/apps/example/app:latest}]\n")
		if _, err := ParseDeployment(input, nil, "", "app", "workers"); err == nil {
			t.Fatalf("accepted additional scheduling:\n%s", input)
		}
	}
}

// TestParseDeploymentRejectsCardinalityAndUnsupportedFields verifies strict stream and subset handling.
func TestParseDeploymentRejectsCardinalityAndUnsupportedFields(t *testing.T) {
	pod := "apiVersion: v1\nkind: Pod\nmetadata: {name: app}\nspec: {containers: [{name: app, image: registry.podmin.internal/apps/example/app:latest}]}\n"
	daemonSet := "apiVersion: apps/v1\nkind: DaemonSet\nmetadata: {name: app}\nspec: {template: {metadata: {}, spec: {nodeSelector: {podmin.dev/nodegroup: workers}, containers: [{name: app, image: registry.podmin.internal/apps/example/app:latest}]}}}\n"
	service := "apiVersion: v1\nkind: Service\nmetadata: {name: app}\nspec: {selector: {app: app}, ports: [{port: 80}]}\n"
	invalid := []string{
		service,
		pod + "---\n" + pod,
		daemonSet + "---\n" + service + "---\n" + service,
		daemonSet + "---\napiVersion: v1\nkind: Service\nmetadata: {name: app, namespace: other}\nspec: {selector: {app: app}, ports: [{port: 80}]}\n",
		daemonSet + "---\napiVersion: v1\nkind: Service\nmetadata: {name: app}\nspec: {selector: {app: app}, ports: [{port: 80, targetPort: http}]}\n",
	}
	for _, input := range invalid {
		if _, err := ParseDeployment([]byte(input), nil, "", "app", "workers"); err == nil {
			t.Errorf("unexpectedly accepted:\n%s", input)
		}
	}
}
