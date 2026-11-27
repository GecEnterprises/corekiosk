package expo.modules.corekioskdiscovery

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.net.wifi.WifiManager
import expo.modules.kotlin.Promise
import expo.modules.kotlin.modules.Module
import expo.modules.kotlin.modules.ModuleDefinition
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.NetworkInterface
import java.net.SocketTimeoutException
import java.net.URL
import java.util.Collections
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.joinAll
import kotlinx.coroutines.launch
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeoutOrNull
import org.json.JSONObject
import kotlin.coroutines.coroutineContext
import kotlin.coroutines.resume

private const val MODULE_NAME = "CorekioskDiscovery"
private const val SERVICE_TYPE = "_corekiosk._tcp."
private const val SSDP_ADDRESS = "239.255.255.250"
private const val SSDP_PORT = 1900
private const val UDP_PORT = 8099
private const val TAG = "CorekioskDiscovery"

class CorekioskDiscoveryModule : Module() {
  private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
  private val activeResults = Collections.synchronizedList(mutableListOf<Map<String, Any?>>())
  private val seenOffers = Collections.synchronizedSet(mutableSetOf<String>())

  @Volatile
  private var scanJob: Job? = null

  @Volatile
  private var activePromise: Promise? = null

  @Volatile
  private var scanGeneration = 0

  @Volatile
  private var multicastLock: WifiManager.MulticastLock? = null

  override fun definition() = ModuleDefinition {
    Name(MODULE_NAME)

    Events("onServerFound", "onScanState")

    AsyncFunction("startScan") { options: Map<String, Any?>, promise: Promise ->
      val timeoutMs = (options["timeoutMs"] as? Number)?.toLong() ?: 3500L
      val methods = (options["methods"] as? List<*>)?.mapNotNull { it as? String } ?: listOf("udp", "mdns", "ssdp")
      startScanInternal(timeoutMs.coerceIn(250L, 30_000L), methods, promise)
    }

    AsyncFunction("stopScan") {
      stopScanInternal()
    }

    OnDestroy {
      runCatching { stopScanInternal() }
      runCatching { multicastLock?.release() }
      multicastLock = null
      runCatching { scope.cancel() }
    }
  }

  private fun startScanInternal(timeoutMs: Long, methods: List<String>, promise: Promise) {
    stopScanInternal()
    val generation = ++scanGeneration
    val normalized = methods.map { it.lowercase() }.toSet()

    activePromise = promise
    activeResults.clear()
    seenOffers.clear()

    scanJob = scope.launch {
      sendScanState(true)
      acquireMulticastLockIfNeeded(normalized)
      try {
        coroutineScope {
          val jobs = mutableListOf<Job>()
          if ("udp" in normalized) jobs += launch { scanUdp(timeoutMs) { emitOffer(generation, it) } }
          if ("mdns" in normalized) jobs += launch { scanMdns(timeoutMs) { emitOffer(generation, it) } }
          if ("ssdp" in normalized) jobs += launch { scanSsdp(timeoutMs) { emitOffer(generation, it) } }
          jobs.joinAll()
        }
      } catch (_: CancellationException) {
        // stopScan()/a new scan cancelled this run; the finally block still resolves.
      } catch (err: Throwable) {
        android.util.Log.w(TAG, "discovery scan failed", err)
      } finally {
        releaseMulticastLock()
        if (generation == scanGeneration) {
          sendScanState(false)
          activePromise?.resolve(activeResults.toList())
          activePromise = null
          scanJob = null
        }
      }
    }
  }

  private fun stopScanInternal() {
    scanGeneration++
    val job = scanJob
    val promise = activePromise
    scanJob = null
    activePromise = null
    job?.cancel()
    if (promise != null) {
      sendScanState(false)
      runCatching { promise.resolve(activeResults.toList()) }
    }
    releaseMulticastLock()
  }

  private fun emitOffer(generation: Int, offer: Map<String, Any?>) {
    if (generation != scanGeneration) return
    val serverId = (offer["serverId"] as? String)?.trim().orEmpty()
    val host = (offer["host"] as? String)?.trim().orEmpty()
    if (serverId.isEmpty() || host.isEmpty()) return
    val key = "$serverId|$host|${offer["port"]}|${offer["source"]}"
    if (!seenOffers.add(key)) return
    activeResults.add(offer)
    safeSendEvent("onServerFound", mapOf("server" to offer))
  }

  private fun sendScanState(scanning: Boolean) {
    safeSendEvent("onScanState", mapOf("scanning" to scanning))
  }

  private fun safeSendEvent(name: String, body: Map<String, Any?>) {
    runCatching { sendEvent(name, body) }
  }

  private fun acquireMulticastLockIfNeeded(methods: Set<String>) {
    if ("ssdp" !in methods) return
    runCatching {
      val ctx = appContext.reactContext ?: return
      val wifi = ctx.applicationContext.getSystemService(Context.WIFI_SERVICE) as? WifiManager ?: return
      val lock = wifi.createMulticastLock("corekiosk-discovery")
      lock.setReferenceCounted(false)
      lock.acquire()
      multicastLock = lock
    }.onFailure { android.util.Log.w(TAG, "multicast lock unavailable", it) }
  }

  private fun releaseMulticastLock() {
    runCatching { multicastLock?.release() }
    multicastLock = null
  }

  private suspend fun scanUdp(timeoutMs: Long, emit: (Map<String, Any?>) -> Unit) {
    val socket = DatagramSocket(null)
    socket.reuseAddress = true
    socket.broadcast = true
    try {
      socket.bind(InetSocketAddress(0))
      val nonce = randomNonce()
      val probe = JSONObject()
        .put("ck", 1)
        .put("t", "probe")
        .put("n", nonce)
        .put("p", 1)
        .toString()
        .toByteArray(Charsets.UTF_8)

      val targets = linkedSetOf<InetAddress>()
      runCatching { targets.add(InetAddress.getByName("255.255.255.255")) }
      runCatching {
        Collections.list(NetworkInterface.getNetworkInterfaces()).forEach { nif ->
          if (!nif.isUp || nif.isLoopback) return@forEach
          nif.interfaceAddresses.forEach { addr ->
            addr.broadcast?.let { targets.add(it) }
          }
        }
      }
      for (target in targets) {
        runCatching { socket.send(DatagramPacket(probe, probe.size, target, UDP_PORT)) }
      }

      val deadline = System.currentTimeMillis() + timeoutMs
      socket.soTimeout = 250
      val buffer = ByteArray(2048)
      while (System.currentTimeMillis() < deadline) {
        coroutineContext.ensureActive()
        val packet = DatagramPacket(buffer, buffer.size)
        try {
          socket.receive(packet)
        } catch (_: SocketTimeoutException) {
          continue
        }
        val json = runCatching {
          JSONObject(String(packet.data, 0, packet.length, Charsets.UTF_8))
        }.getOrNull() ?: continue
        if (json.optInt("ck", 0) != 1 || json.optString("t") != "offer") continue
        if (json.optString("n") != nonce) continue
        val serverId = json.optString("id").trim()
        if (serverId.isEmpty()) continue
        val host = normalizeHost(packet.address?.hostAddress.orEmpty())
        if (host.isEmpty()) continue
        emit(
          mapOf(
            "serverId" to serverId,
            "name" to json.optString("name"),
            "appName" to json.optString("app"),
            "host" to host,
            "port" to json.optInt("port", 8080),
            "source" to "udp",
            "protocolVersion" to json.optInt("proto", 0),
            "version" to json.optString("v"),
            "lastSeenMs" to System.currentTimeMillis()
          )
        )
      }
    } finally {
      runCatching { socket.close() }
    }
  }

  private suspend fun scanMdns(timeoutMs: Long, emit: (Map<String, Any?>) -> Unit) {
    val ctx = appContext.reactContext ?: return
    val nsd = ctx.applicationContext.getSystemService(Context.NSD_SERVICE) as? NsdManager ?: return
    var activeListener: NsdManager.DiscoveryListener? = null

    withContext(Dispatchers.Main.immediate) {
      try {
        withTimeoutOrNull(timeoutMs) {
          suspendCancellableCoroutine<Unit> { cont ->
            val pending = java.util.ArrayDeque<NsdServiceInfo>()
            var resolving = false

            fun resolveNext() {
              if (resolving) return
              val info = pending.poll() ?: return
              resolving = true
              try {
                nsd.resolveService(info, object : NsdManager.ResolveListener {
                  override fun onResolveFailed(serviceInfo: NsdServiceInfo?, errorCode: Int) {
                    resolving = false
                    resolveNext()
                  }

                  override fun onServiceResolved(serviceInfo: NsdServiceInfo?) {
                    resolving = false
                    if (serviceInfo != null) emitMdns(serviceInfo, emit)
                    resolveNext()
                  }
                })
              } catch (_: Throwable) {
                resolving = false
                resolveNext()
              }
            }

            val listener = object : NsdManager.DiscoveryListener {
              override fun onDiscoveryStarted(serviceType: String?) {}

              override fun onServiceFound(serviceInfo: NsdServiceInfo?) {
                if (serviceInfo == null) return
                if (!serviceInfo.serviceType.orEmpty().contains("_corekiosk._tcp", ignoreCase = true)) return
                pending.add(serviceInfo)
                resolveNext()
              }

              override fun onServiceLost(serviceInfo: NsdServiceInfo?) {}

              override fun onDiscoveryStopped(serviceType: String?) {
                if (cont.isActive) cont.resume(Unit)
              }

              override fun onStartDiscoveryFailed(serviceType: String?, errorCode: Int) {
                if (cont.isActive) cont.resume(Unit)
              }

              override fun onStopDiscoveryFailed(serviceType: String?, errorCode: Int) {
                if (cont.isActive) cont.resume(Unit)
              }
            }
            activeListener = listener
            cont.invokeOnCancellation {
              runCatching { nsd.stopServiceDiscovery(listener) }
            }
            try {
              nsd.discoverServices(SERVICE_TYPE, NsdManager.PROTOCOL_DNS_SD, listener)
            } catch (_: Throwable) {
              if (cont.isActive) cont.resume(Unit)
            }
          }
        }
      } finally {
        activeListener?.let { runCatching { nsd.stopServiceDiscovery(it) } }
      }
    }
  }

  private fun emitMdns(info: NsdServiceInfo, emit: (Map<String, Any?>) -> Unit) {
    val host = normalizeHost(info.host?.hostAddress.orEmpty())
    if (host.isEmpty()) return
    val attrs = info.attributes ?: emptyMap()
    fun attr(key: String): String? = attrs[key]?.let { String(it, Charsets.UTF_8) }?.trim()?.takeIf { it.isNotEmpty() }
    val serverId = attr("id") ?: return
    emit(
      mapOf(
        "serverId" to serverId,
        "name" to cleanInstance(info.serviceName.orEmpty()),
        "appName" to attr("app"),
        "host" to host,
        "port" to info.port,
        "source" to "mdns",
        "protocolVersion" to (attr("proto")?.toIntOrNull() ?: 0),
        "version" to attr("v"),
        "lastSeenMs" to System.currentTimeMillis()
      )
    )
  }

  private suspend fun scanSsdp(timeoutMs: Long, emit: (Map<String, Any?>) -> Unit) {
    val socket = DatagramSocket()
    try {
      socket.soTimeout = 250
      val group = InetAddress.getByName(SSDP_ADDRESS)
      val request = (
        "M-SEARCH * HTTP/1.1\r\n" +
          "HOST: $SSDP_ADDRESS:$SSDP_PORT\r\n" +
          "MAN: \"ssdp:discover\"\r\n" +
          "MX: 2\r\n" +
          "ST: urn:gec:corekiosk:server:1\r\n\r\n"
        ).toByteArray(Charsets.UTF_8)
      socket.send(DatagramPacket(request, request.size, group, SSDP_PORT))

      val deadline = System.currentTimeMillis() + timeoutMs
      val buffer = ByteArray(2048)
      while (System.currentTimeMillis() < deadline) {
        coroutineContext.ensureActive()
        val packet = DatagramPacket(buffer, buffer.size)
        try {
          socket.receive(packet)
        } catch (_: SocketTimeoutException) {
          continue
        }
        val text = String(packet.data, 0, packet.length, Charsets.UTF_8)
        if (!text.startsWith("HTTP/1.1 200", ignoreCase = true)) continue
        val headers = parseHeaders(text)
        val serverId = headers["x-ck-id"]
          ?: headers["usn"]?.removePrefix("urn:gec:corekiosk:")
          ?: continue
        val location = headers["location"] ?: continue
        val url = runCatching { URL(location) }.getOrNull() ?: continue
        val host = normalizeHost(url.host.ifEmpty { packet.address?.hostAddress.orEmpty() })
        if (host.isEmpty()) continue
        emit(
          mapOf(
            "serverId" to serverId.trim(),
            "name" to headers["x-ck-name"].orEmpty(),
            "host" to host,
            "port" to if (url.port > 0) url.port else 80,
            "source" to "ssdp",
            "lastSeenMs" to System.currentTimeMillis()
          )
        )
      }
    } finally {
      runCatching { socket.close() }
    }
  }

  private fun parseHeaders(text: String): Map<String, String> {
    val out = mutableMapOf<String, String>()
    for (line in text.split("\r\n").drop(1)) {
      val idx = line.indexOf(':')
      if (idx <= 0) continue
      val key = line.substring(0, idx).trim().lowercase()
      val value = line.substring(idx + 1).trim()
      if (key.isNotEmpty()) out[key] = value
    }
    return out
  }

  private fun normalizeHost(raw: String): String {
    var host = raw.trim()
    if (host.startsWith("::ffff:")) host = host.removePrefix("::ffff:")
    if (host.startsWith("[") && host.endsWith("]")) host = host.substring(1, host.length - 1)
    return host
  }

  private fun cleanInstance(instance: String): String {
    var value = instance.trim().replace("\\032", " ")
    val idx = value.lastIndexOf(" [")
    if (idx > 0 && value.endsWith("]")) value = value.substring(0, idx)
    return value
  }

  private fun randomNonce(): String {
    val bytes = ByteArray(8)
    java.security.SecureRandom().nextBytes(bytes)
    return bytes.joinToString("") { "%02x".format(it) }
  }
}
