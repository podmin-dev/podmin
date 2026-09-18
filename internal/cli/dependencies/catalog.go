// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package dependencies

// dependencySource identifies the upstream metadata source for a dependency.
type dependencySource uint8

const (
	githubReleaseSource dependencySource = iota
	aptRepositorySource
)

// versionFormat identifies the version format published by a release source.
type versionFormat uint8

const (
	semanticVersion versionFormat = iota
	datedVersion
)

// Dependency describes one version-constrained upstream artifact.
type Dependency struct {
	Key               string
	Major             int
	Minor             int
	Source            dependencySource
	VersionFormat     versionFormat
	Releases          string
	PackageIndex      string
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
		Key: "gvisor", VersionFormat: datedVersion,
		Releases:          "https://api.github.com/repos/google/gvisor/releases?per_page=100",
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
		Key: "crictl", Major: 1, Minor: 36,
		Releases:          "https://api.github.com/repos/kubernetes-sigs/cri-tools/releases",
		Architectures:     map[string]string{"amd64": "amd64", "arm64": "arm64"},
		AssetName:         "crictl-v{version}-linux-{architecture}.tar.gz",
		AssetURL:          "https://github.com/kubernetes-sigs/cri-tools/releases/download/v{version}/{asset}",
		ChecksumURL:       "{url}.sha512",
		ChecksumName:      "{asset}",
		ChecksumAlgorithm: "sha512",
		ObjectName:        "crictl.tar.gz",
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
		Key: "libpq5", Source: aptRepositorySource,
		PackageIndex:      "https://deb.debian.org/debian/dists/trixie/main/binary-{architecture}/Packages.gz",
		Architectures:     map[string]string{"amd64": "amd64", "arm64": "arm64"},
		AssetURL:          "https://deb.debian.org/debian/{asset}",
		ChecksumAlgorithm: "sha256",
		ObjectName:        "libpq5.deb",
	},
	{
		Key: "fluent-bit", Major: 5, Source: aptRepositorySource,
		PackageIndex:      "https://packages.fluentbit.io/debian/trixie/dists/trixie/main/binary-{architecture}/Packages",
		Architectures:     map[string]string{"amd64": "amd64", "arm64": "arm64"},
		AssetURL:          "https://packages.fluentbit.io/debian/trixie/{asset}",
		ChecksumAlgorithm: "sha512",
		ObjectName:        "fluent-bit.deb",
	},
	{
		Key:               "podmin-agent",
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
