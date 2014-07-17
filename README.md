# ManiPt
Badly named prototype of Manifold.

## Testing this... thing

Grab this with `go get -u github.com/robxu9/manipt`, and then run with
`manipt -h` for help options.

```
Usage: manipt [flags] (PORT)
Embedded webapp will be started on specified PORT
  -addr="127.0.0.1:8500": set the address of the consul agent
  -bind="0.0.0.0": bind to interface
```

The web app is embedded into manipt. At the moment, all it says is
`Hello World from HOSTNAME!`. Said `HOSTNAME` will change as the master
changes.

## Architecture

ManiPt is a "distributed proxy service". We have a cluster of nodes that all
have ManiPt running on it, as well as a Consul agent, as we build on Consul.

Using Consul, all ManiPt nodes choose a master. The master node is the only
node that will serve the web application. All other nodes, referred to as the
proxy nodes, will forward requests they receive on their end to the master
node.

If the master changes (or there is no known leader), all nodes are running a
local copy of the web application, so they can serve it until a new master is
availabkle.

The difference from a load balancer is that we don't transition between
different nodes to handle each request. We always forward to the master,
and rely on Consul for when it's time to switch masters.