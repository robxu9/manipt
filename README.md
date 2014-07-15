# ManiPt
Badly named prototype of Manifold.

## Architecture

In a way, ManiPt acts as a cluster of apps on top of Consul. It relies on
Consul (and by extension, the Raft consensus protocol) to select a
leader node will handle connections to these apps. The slave nodes will also
handle connections, but will proxy them to the leader node chosen. In case any
node goes down, there are others to pick up the slack until the new leader is
chosen.

The difference from a load balancer is that we don't transition between
different nodes to handle each request. We always forward to the leader,
and let Consul handle switching leaders.

## Projected Usage

Each node is part of a Consul cluster, so they all run agents. They also
run a ManiPt agent, which talks with the Consul agent on the node to
determine leader status & etc.

Each node is also running an instance of the app (todo: launch when
needed?). Depending on the node status (leader or slave), it will either
ask the app to handle it or forward it to the leader node.
