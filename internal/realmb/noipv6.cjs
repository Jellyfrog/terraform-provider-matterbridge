// Preloaded with NODE_OPTIONS=--require only on a host without IPv6 (for example a sandbox with a kernel without
// IPv6). matter.js binds an IPv4 and an IPv6 UDP socket on the same port. This script makes the IPv6 sockets IPv4
// sockets, binds all UDP sockets with reuseAddr so that they can share the port, and drops IPv6 multicast and
// sends. Matterbridge then starts, but Matter controllers cannot use it. The provider tests need only the frontend.
const dgram = require("dgram");
const isV6 = (x) => typeof x === "string" && x.includes(":");
const create = dgram.createSocket;
dgram.createSocket = function (opts, cb) {
  const o = typeof opts === "string" ? { type: opts } : { ...opts };
  o.reuseAddr = true;
  if (o.type === "udp6") {
    o.type = "udp4";
    delete o.ipv6Only;
  }
  const s = create.call(this, o, cb);
  const bind = s.bind;
  s.bind = function (...a) {
    if (a[0] && typeof a[0] === "object" && isV6(a[0].address)) a[0] = { ...a[0], address: "0.0.0.0" };
    else if (isV6(a[1])) a[1] = "0.0.0.0";
    return bind.apply(this, a);
  };
  for (const m of ["addMembership", "dropMembership", "setMulticastInterface"]) {
    const f = s[m];
    s[m] = function (...a) {
      if (a.some(isV6)) return;
      return f.apply(this, a);
    };
  }
  const send = s.send;
  s.send = function (...a) {
    if (a.some(isV6)) {
      const done = a.find((x) => typeof x === "function");
      if (done) process.nextTick(done, null);
      return;
    }
    return send.apply(this, a);
  };
  return s;
};
