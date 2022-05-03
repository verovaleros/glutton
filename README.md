# Glutton
![Tests](https://github.com/mushorg/glutton/actions/workflows/workflow.yml/badge.svg)
[![GoDoc](https://godoc.org/github.com/mushorg/glutton?status.svg)](https://godoc.org/github.com/mushorg/glutton)

Glutton is a low interaction honeypot written in GO that listens in all ports for incoming connections. Every connection is matched against a list of predefined rules which determine what to do with the incoming traffic. If a rule is matched it can be drop, proxied to a different service, or processed by Glutton.

### Requirements

Glutton requires `go 1.17`.

Install required system packages:
```
apt-get install libnetfilter-queue-dev libpcap-dev iptables lsof
```

Arch:
```
pacman -S libnetfilter_queue libpcap iptables lsof
```

### Configurations

To change your SSH server default port (i.e. 5001, see `rules.yaml`) and restart sshd:
```
sed -i 's/[# ]*Port .*/Port 5001/g' /etc/ssh/sshd_config
```

### Build and Run

Build glutton:
```
make build
```

To run/test glutton:
```
bin/server
```

To run/test glutton storing the output:
```
bin/server -l /var/log/glutton
```

# Use as Proxy

Glutton provide SSH and a TCP proxy. SSH proxy works as a MITM between attacker and server to log everything in plain text. TCP proxy does not provide facility for logging yet. Examples can be found [here](https://github.com/mushorg/glutton/tree/master/examples).
