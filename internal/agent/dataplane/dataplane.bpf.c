// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

//go:build ignore

#include <linux/bpf.h>
#include <linux/if_ether.h>
#include <linux/in.h>
#include <linux/in6.h>
#include <linux/ipv6.h>
#include <linux/pkt_cls.h>
#include <linux/tcp.h>
#include <linux/udp.h>
#include <bpf/bpf_endian.h>
#include <bpf/bpf_helpers.h>

struct service_key { __u8 vip[16]; __be16 port; __u8 protocol; __u8 pad; };
struct service_value { __u32 backend_offset; __u32 backend_count; };
struct backend_value { __u8 address[16]; __be16 port; __u16 pad; };
struct flow_key { __u8 source[16]; __u8 destination[16]; __be16 source_port; __be16 destination_port; __u8 protocol; __u8 pad[3]; };
struct forward_value { __u8 backend[16]; __be16 backend_port; __be16 snat_port; };
struct reverse_value { __u8 client[16]; __u8 vip[16]; __be16 client_port; __be16 vip_port; };

struct { __uint(type, BPF_MAP_TYPE_HASH); __uint(max_entries, 65536); __type(key, struct service_key); __type(value, struct service_value); } services SEC(".maps");
struct { __uint(type, BPF_MAP_TYPE_ARRAY); __uint(max_entries, 65536); __type(key, __u32); __type(value, struct backend_value); } backends SEC(".maps");
struct { __uint(type, BPF_MAP_TYPE_HASH); __uint(max_entries, 65536); __type(key, __u8[16]); __type(value, __u8); } service_vips SEC(".maps");
struct { __uint(type, BPF_MAP_TYPE_ARRAY); __uint(max_entries, 1); __type(key, __u32); __type(value, __u8[16]); } config SEC(".maps");
struct { __uint(type, BPF_MAP_TYPE_LRU_HASH); __uint(max_entries, 262144); __type(key, struct flow_key); __type(value, struct forward_value); } forward_flows SEC(".maps");
struct { __uint(type, BPF_MAP_TYPE_LRU_HASH); __uint(max_entries, 262144); __type(key, struct flow_key); __type(value, struct reverse_value); } reverse_flows SEC(".maps");

static __always_inline __u32 hash_bytes(const __u8 *p, __u32 length, __u32 hash) {
#pragma clang loop unroll(full)
  for (__u32 i = 0; i < 16; i++) if (i < length) hash = (hash ^ p[i]) * 16777619;
  return hash;
}

static __always_inline void copy16(__u8 *to, const __u8 *from) {
#pragma clang loop unroll(full)
  for (__u32 i = 0; i < 16; i++) to[i] = from[i];
}

static __always_inline __u8 equal16(const __u8 *left, const __u8 *right) {
  __u8 equal = 1;
#pragma clang loop unroll(full)
  for (__u32 i = 0; i < 16; i++) equal &= left[i] == right[i];
  return equal;
}

static __always_inline __u8 reverse_matches(const struct reverse_value *reverse,
                                            const struct flow_key *flow) {
  return equal16(reverse->client, flow->source) && equal16(reverse->vip, flow->destination) &&
      reverse->client_port == flow->source_port && reverse->vip_port == flow->destination_port;
}

static __always_inline __be16 checksum_replace(__be16 checksum, const __u8 *old_address,
                                                const __u8 *new_address, __be16 old_port,
                                                __be16 new_port) {
  __u32 sum = (~bpf_ntohs(checksum)) & 0xffff;
#pragma clang loop unroll(full)
  for (__u32 i = 0; i < 16; i += 2) {
    __u16 old_word = ((__u16)old_address[i] << 8) | old_address[i + 1];
    __u16 new_word = ((__u16)new_address[i] << 8) | new_address[i + 1];
    sum += (~old_word) & 0xffff;
    sum += new_word;
  }
  sum += (~bpf_ntohs(old_port)) & 0xffff;
  sum += bpf_ntohs(new_port);
  sum = (sum & 0xffff) + (sum >> 16);
  sum = (sum & 0xffff) + (sum >> 16);
  __u16 result = (__u16)~sum;
  return bpf_htons(result ? result : 0xffff);
}

SEC("tcx/ingress")
int podmin_ingress(struct __sk_buff *skb) {
  void *data = (void *)(long)skb->data;
  void *end = (void *)(long)skb->data_end;
  struct ethhdr *eth = data;
  struct ipv6hdr *ip6 = data + sizeof(*eth);
  if ((void *)(ip6 + 1) > end || eth->h_proto != bpf_htons(ETH_P_IPV6)) return TC_ACT_OK;

  __u8 *node = bpf_map_lookup_elem(&config, &(__u32){0});
  if (!node) return TC_ACT_SHOT;
  if (ip6->nexthdr == 44) {
    __u8 *known = bpf_map_lookup_elem(&service_vips, &ip6->daddr.s6_addr);
    return known ? TC_ACT_SHOT : TC_ACT_OK;
  }
  if (ip6->nexthdr != IPPROTO_TCP && ip6->nexthdr != IPPROTO_UDP) {
    __u8 *known = bpf_map_lookup_elem(&service_vips, &ip6->daddr.s6_addr);
    return known ? TC_ACT_SHOT : TC_ACT_OK;
  }
  struct udphdr *transport = (void *)(ip6 + 1);
  if ((void *)(transport + 1) > end) return TC_ACT_SHOT;
  if (ip6->nexthdr == IPPROTO_TCP && (void *)((struct tcphdr *)transport + 1) > end) return TC_ACT_SHOT;
  __be16 *checksum = ip6->nexthdr == IPPROTO_TCP
      ? &((struct tcphdr *)transport)->check : &transport->check;
  if (ip6->nexthdr == IPPROTO_UDP && *checksum == 0) return TC_ACT_SHOT;

  struct flow_key flow = {};
  copy16(flow.source, ip6->saddr.s6_addr); copy16(flow.destination, ip6->daddr.s6_addr);
  flow.source_port = transport->source; flow.destination_port = transport->dest; flow.protocol = ip6->nexthdr;
  struct reverse_value *reverse = bpf_map_lookup_elem(&reverse_flows, &flow);
  if (reverse) {
    *checksum = checksum_replace(*checksum, ip6->saddr.s6_addr, reverse->vip, transport->source, reverse->vip_port);
    *checksum = checksum_replace(*checksum, ip6->daddr.s6_addr, reverse->client, transport->dest, reverse->client_port);
    copy16(ip6->saddr.s6_addr, reverse->vip); copy16(ip6->daddr.s6_addr, reverse->client);
    transport->source = reverse->vip_port; transport->dest = reverse->client_port;
    return TC_ACT_OK;
  }

  struct service_key service_key = {};
  copy16(service_key.vip, ip6->daddr.s6_addr); service_key.port = transport->dest; service_key.protocol = ip6->nexthdr;
  struct service_value *service = bpf_map_lookup_elem(&services, &service_key);
  if (!service) return TC_ACT_OK;
  if (!service->backend_count) return TC_ACT_SHOT;
  struct forward_value *forward = bpf_map_lookup_elem(&forward_flows, &flow);
  if (!forward) {
    __u32 hash = hash_bytes(flow.source, 16, 2166136261U);
    hash = hash_bytes(flow.destination, 16, hash) ^ ((__u32)flow.source_port << 16) ^ flow.destination_port ^ flow.protocol;
    __u32 index = service->backend_offset + hash % service->backend_count;
    struct backend_value *backend = bpf_map_lookup_elem(&backends, &index);
    if (!backend) return TC_ACT_SHOT;
    struct forward_value candidate = {};
    copy16(candidate.backend, backend->address); candidate.backend_port = backend->port;
    candidate.snat_port = bpf_htons(30000 + hash % 2768);
    /* Probe a bounded deterministic sequence to avoid stealing another live reverse tuple. */
#pragma clang loop unroll(full)
    for (__u32 attempt = 0; attempt < 32; attempt++) {
      struct flow_key reverse_key = {};
      copy16(reverse_key.source, candidate.backend); copy16(reverse_key.destination, node);
      reverse_key.source_port = candidate.backend_port; reverse_key.destination_port = candidate.snat_port; reverse_key.protocol = flow.protocol;
      struct reverse_value *existing_reverse = bpf_map_lookup_elem(&reverse_flows, &reverse_key);
      if (existing_reverse) {
        /* A concurrent first packet or one-sided forward LRU eviction may own this tuple for this flow. */
        if (reverse_matches(existing_reverse, &flow)) {
          if (bpf_map_update_elem(&forward_flows, &flow, &candidate, BPF_ANY)) return TC_ACT_SHOT;
          forward = bpf_map_lookup_elem(&forward_flows, &flow); break;
        }
      } else {
        struct reverse_value reverse_value = {};
        copy16(reverse_value.client, flow.source); copy16(reverse_value.vip, flow.destination);
        reverse_value.client_port = flow.source_port; reverse_value.vip_port = flow.destination_port;
        if (bpf_map_update_elem(&reverse_flows, &reverse_key, &reverse_value, BPF_NOEXIST)) continue;
        if (bpf_map_update_elem(&forward_flows, &flow, &candidate, BPF_ANY)) { bpf_map_delete_elem(&reverse_flows, &reverse_key); return TC_ACT_SHOT; }
        forward = bpf_map_lookup_elem(&forward_flows, &flow); break;
      }
      candidate.snat_port = bpf_htons(30000 + (hash + attempt + 1) % 2768);
    }
    if (!forward) return TC_ACT_SHOT;
  }
  /* Repair one-sided LRU eviction before translating another forward packet. */
  struct flow_key reverse_key = {};
  copy16(reverse_key.source, forward->backend); copy16(reverse_key.destination, node);
  reverse_key.source_port = forward->backend_port; reverse_key.destination_port = forward->snat_port; reverse_key.protocol = flow.protocol;
  struct reverse_value *forward_reverse = bpf_map_lookup_elem(&reverse_flows, &reverse_key);
  if (!forward_reverse) {
    struct reverse_value reverse_value = {};
    copy16(reverse_value.client, flow.source); copy16(reverse_value.vip, flow.destination);
    reverse_value.client_port = flow.source_port; reverse_value.vip_port = flow.destination_port;
    if (bpf_map_update_elem(&reverse_flows, &reverse_key, &reverse_value, BPF_NOEXIST)) {
      bpf_map_delete_elem(&forward_flows, &flow);
      return TC_ACT_SHOT;
    }
    forward_reverse = bpf_map_lookup_elem(&reverse_flows, &reverse_key);
  }
  if (!forward_reverse || !reverse_matches(forward_reverse, &flow)) {
    bpf_map_delete_elem(&forward_flows, &flow);
    return TC_ACT_SHOT;
  }
  *checksum = checksum_replace(*checksum, ip6->saddr.s6_addr, node, transport->source, forward->snat_port);
  *checksum = checksum_replace(*checksum, ip6->daddr.s6_addr, forward->backend, transport->dest, forward->backend_port);
  copy16(ip6->saddr.s6_addr, node); copy16(ip6->daddr.s6_addr, forward->backend);
  transport->source = forward->snat_port; transport->dest = forward->backend_port;
  return TC_ACT_OK;
}

char LICENSE[] SEC("license") = "Apache-2.0";
