# The egress lane's probe, run in an add-on engine's network namespace (nsenter -n) with the VM's python3:
#   lookup <name> [address]  asks funcd's forwarder for <name>'s A record as a worker's resolver does, then opens TLS to
#                            the first address it answered, else to <address>, and prints what each step got.
import socket
import ssl
import sys


def lookup(name):
    labels = b"".join(bytes([len(label)]) + label.encode() for label in name.split("."))
    query = b"\x00\x01\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00" + labels + b"\x00\x00\x01\x00\x01"
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.settimeout(3)
    s.sendto(query, ("10.63.0.1", 53))
    answer = s.recv(4096)
    rcode = answer[3] & 15
    print("lookup", name, "rcode", rcode)
    if rcode != 0:
        return None
    i = 12
    while answer[i] != 0:
        i += answer[i] + 1
    i += 5
    for _ in range(int.from_bytes(answer[6:8], "big")):
        if answer[i] & 0xC0 == 0xC0:
            i += 2
        else:
            while answer[i] != 0:
                i += answer[i] + 1
            i += 1
        kind, length = int.from_bytes(answer[i:i + 2], "big"), int.from_bytes(answer[i + 8:i + 10], "big")
        i += 10
        if kind == 1 and length == 4:
            return socket.inet_ntoa(answer[i:i + 4])
        i += length
    return None


def connect(name, address):
    try:
        with socket.create_connection((address, 443), timeout=5) as conn:
            with ssl.create_default_context().wrap_socket(conn, server_hostname=name) as tls:
                print("tls", name, tls.version())
    except OSError as e:
        print("connect", name, "failed", e)


if sys.argv[1] == "lookup":
    found = lookup(sys.argv[2])
    target = found or (sys.argv[3] if len(sys.argv) > 3 else None)
    if target:
        connect(sys.argv[2], target)
