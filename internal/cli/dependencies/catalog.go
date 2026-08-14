// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dependencies

// releaseStyle identifies nonstandard release selection.
type releaseStyle uint8

const (
	semverRelease releaseStyle = iota
	gvisorRelease
	agentRelease
)

// Dependency describes one version-constrained upstream artifact.
type Dependency struct {
	Key               string
	Major             int
	Minor             int
	ReleaseStyle      releaseStyle
	Releases          string
	Architectures     map[string]string
	AssetName         string
	AssetURL          string
	ChecksumURL       string
	ChecksumName      string
	ChecksumAlgorithm string
	ObjectName        string
}

// Catalog is the complete runtime dependency catalog. URL fields accept
// {version}, {architecture}, {asset}, and {url} substitutions.
var Catalog = []Dependency{
	{
		Key: "containerd", Major: 2,
		Releases:          "https://api.github.com/repos/containerd/containerd/releases",
		Architectures:     map[string]string{"amd64": "amd64", "arm64": "arm64"},
		AssetName:         "containerd-{version}-linux-{architecture}.tar.gz",
		AssetURL:          "https://github.com/containerd/containerd/releases/download/v{version}/{asset}",
		ChecksumURL:       "{url}.sha256sum",
		ChecksumName:      "{asset}",
		ChecksumAlgorithm: "sha256",
		ObjectName:        "containerd.tar.gz",
	},
	{
		Key: "gvisor", ReleaseStyle: gvisorRelease,
		Releases:          "https://api.github.com/repos/google/gvisor/tags?per_page=100",
		Architectures:     map[string]string{"amd64": "x86_64", "arm64": "aarch64"},
		AssetName:         "gvisor.tar.bz2",
		AssetURL:          "https://storage.googleapis.com/gvisor/releases/release/{version}/{architecture}/{asset}",
		ChecksumURL:       "{url}.sha512",
		ChecksumName:      "{asset}",
		ChecksumAlgorithm: "sha512",
		ObjectName:        "gvisor.tar.bz2",
	},
	{
		Key: "cni-plugins", Major: 1,
		Releases:          "https://api.github.com/repos/containernetworking/plugins/releases",
		Architectures:     map[string]string{"amd64": "amd64", "arm64": "arm64"},
		AssetName:         "cni-plugins-linux-{architecture}-v{version}.tgz",
		AssetURL:          "https://github.com/containernetworking/plugins/releases/download/v{version}/{asset}",
		ChecksumURL:       "{url}.sha256",
		ChecksumName:      "{asset}",
		ChecksumAlgorithm: "sha256",
		ObjectName:        "cni-plugins.tar.gz",
	},
	{
		Key: "kubelet", Major: 1, Minor: 36,
		Releases:          "https://api.github.com/repos/kubernetes/kubernetes/releases",
		Architectures:     map[string]string{"amd64": "amd64", "arm64": "arm64"},
		AssetName:         "kubelet",
		AssetURL:          "https://dl.k8s.io/release/v{version}/bin/linux/{architecture}/kubelet",
		ChecksumURL:       "{url}.sha512",
		ChecksumName:      "{asset}",
		ChecksumAlgorithm: "sha512",
		ObjectName:        "kubelet",
	},
	{
		Key: "coredns", Major: 1,
		Releases:          "https://api.github.com/repos/coredns/coredns/releases",
		Architectures:     map[string]string{"amd64": "amd64", "arm64": "arm64"},
		AssetName:         "coredns_{version}_linux_{architecture}.tgz",
		AssetURL:          "https://github.com/coredns/coredns/releases/download/v{version}/{asset}",
		ChecksumURL:       "{url}.sha256",
		ChecksumName:      "{asset}",
		ChecksumAlgorithm: "sha256",
		ObjectName:        "coredns.tar.gz",
	},
	{
		Key: "zot", Major: 2,
		Releases:          "https://api.github.com/repos/project-zot/zot/releases",
		Architectures:     map[string]string{"amd64": "amd64", "arm64": "arm64"},
		AssetName:         "zot-linux-{architecture}-minimal",
		AssetURL:          "https://github.com/project-zot/zot/releases/download/v{version}/{asset}",
		ChecksumURL:       "https://github.com/project-zot/zot/releases/download/v{version}/checksums.sha256.txt",
		ChecksumName:      "{asset}",
		ChecksumAlgorithm: "sha256",
		ObjectName:        "zot",
	},
	{
		Key: "podmin-agent", ReleaseStyle: agentRelease,
		Releases:          "https://api.github.com/repos/podmin-dev/podmin/releases",
		Architectures:     map[string]string{"amd64": "amd64", "arm64": "arm64"},
		AssetName:         "podmin-agent_{version}_linux_{architecture}.tar.gz",
		AssetURL:          "https://github.com/podmin-dev/podmin/releases/download/v{version}/{asset}",
		ChecksumURL:       "https://github.com/podmin-dev/podmin/releases/download/v{version}/podmin_{version}_checksums.txt",
		ChecksumName:      "{asset}",
		ChecksumAlgorithm: "sha512",
		ObjectName:        "podmin-agent.tar.gz",
	},
}
