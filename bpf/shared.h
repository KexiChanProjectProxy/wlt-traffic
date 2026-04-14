#ifndef TRAFFIC_SHARED_H
#define TRAFFIC_SHARED_H

#include <linux/bpf.h>
#include <linux/if_ether.h>
#include <linux/pkt_cls.h>
#include <bpf/bpf_helpers.h>

struct traffic_key {
    __u32 ifindex;
    __u8  mac[6];
};

struct traffic_counter {
    __u64 bytes;
    __u64 packets;
    __u64 ingress_bytes;
    __u64 ingress_packets;
    __u64 egress_bytes;
    __u64 egress_packets;
};

#endif
