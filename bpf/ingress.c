// SPDX-License-Identifier: GPL-2.0 OR BSD-2-Clause

#include "shared.h"

struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __type(key, struct traffic_key);
    __type(value, struct traffic_counter);
    __uint(max_entries, 262144);
} traffic_map SEC(".maps");

SEC("tc/ingress")
int handle_ingress(struct __sk_buff *skb)
{
    void *data = (void *)(long)skb->data;
    void *data_end = (void *)(long)skb->data_end;

    struct ethhdr *eth = data;
    if ((void *)(eth + 1) > data_end)
        return TC_ACT_OK;

    struct traffic_key key = {
        .ifindex = skb->ifindex,
    };
    __builtin_memcpy(key.mac, eth->h_dest, 6);

    struct traffic_counter *counter = bpf_map_lookup_elem(&traffic_map, &key);
    if (!counter) {
        struct traffic_counter new_counter = {};
        bpf_map_update_elem(&traffic_map, &key, &new_counter, BPF_ANY);
        counter = bpf_map_lookup_elem(&traffic_map, &key);
        if (!counter)
            return TC_ACT_OK;
    }

    __sync_fetch_and_add(&counter->bytes, skb->len);
    __sync_fetch_and_add(&counter->packets, 1);
    __sync_fetch_and_add(&counter->ingress_bytes, skb->len);
    __sync_fetch_and_add(&counter->ingress_packets, 1);

    return TC_ACT_OK;
}

SEC("tc/egress")
int handle_egress(struct __sk_buff *skb)
{
    void *data = (void *)(long)skb->data;
    void *data_end = (void *)(long)skb->data_end;

    struct ethhdr *eth = data;
    if ((void *)(eth + 1) > data_end)
        return TC_ACT_OK;

    struct traffic_key key = {
        .ifindex = skb->ifindex,
    };
    __builtin_memcpy(key.mac, eth->h_source, 6);

    struct traffic_counter *counter = bpf_map_lookup_elem(&traffic_map, &key);
    if (!counter) {
        struct traffic_counter new_counter = {};
        bpf_map_update_elem(&traffic_map, &key, &new_counter, BPF_ANY);
        counter = bpf_map_lookup_elem(&traffic_map, &key);
        if (!counter)
            return TC_ACT_OK;
    }

    __sync_fetch_and_add(&counter->bytes, skb->len);
    __sync_fetch_and_add(&counter->packets, 1);
    __sync_fetch_and_add(&counter->egress_bytes, skb->len);
    __sync_fetch_and_add(&counter->egress_packets, 1);

    return TC_ACT_OK;
}
