# @corekiosk/discovery-rn

Native LAN discovery adapter for corekiosk companion tablets.

- **UDP broadcast** probe/offer on `8099/udp` (primary)
- **mDNS / DNS-SD** `_corekiosk._tcp` via Android `NsdManager` (secondary)
- **SSDP** active `M-SEARCH` for `urn:gec:corekiosk:server:1` (optional)

```ts
import { createDiscoveryAdapter } from "@corekiosk/discovery-rn";
import { KioskProvider } from "@corekiosk/rn";

const discovery = createDiscoveryAdapter({ methods: ["udp", "mdns"] });

<KioskProvider discovery={discovery} /* ... */ />
```

Discovery results are hints only. `@corekiosk/rn` always verifies `server_id` and
`server_pub` through `/v1/health` before reconnecting, and first-time pairing still
requires the six-digit code shown on the kiosk.

Android permissions (`CHANGE_WIFI_MULTICAST_STATE`, `ACCESS_NETWORK_STATE`,
`ACCESS_WIFI_STATE`) are merged by the module manifest. The app must be rebuilt after
adding this package (`expo prebuild` / `corekiosk android prebuild`).
