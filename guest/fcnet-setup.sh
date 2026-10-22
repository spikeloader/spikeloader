#!/bin/bash
# Runs inside each microVM (replaces the CI rootfs's fcnet-setup.sh).
# IP comes from the MAC: 06:00:AA:BB:CC:DD -> AA.BB.CC.DD/30; gateway = IP-1 (the TAP on the host).
for dev in $(ls /sys/class/net | grep -v lo); do
    mac_ip=$(ip link show dev "$dev" | grep link/ether \
        | grep -Po "(?<=06:00:)([0-9a-f]{2}:?){4}") || continue
    ip=$(printf "%d.%d.%d.%d" $(echo "0x${mac_ip}" | sed "s/:/ 0x/g"))
    IFS=. read -r a b c d <<<"$ip"
    ip addr add "$ip/30" dev "$dev"
    ip link set "$dev" up
    ip route replace default via "$a.$b.$c.$((d - 1))" dev "$dev"
done

name=$(grep -o 'fclab.hostname=[^ ]*' /proc/cmdline | cut -d= -f2)
if [ -n "$name" ]; then
    echo "$name" >/proc/sys/kernel/hostname
    echo "$name" >/etc/hostname
    echo "127.0.1.1 $name" >>/etc/hosts
fi
