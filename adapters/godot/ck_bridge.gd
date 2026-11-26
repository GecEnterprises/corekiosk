extends Node
class_name CKBridge

## Thin Godot 4 client for corekiosk's loopback WebSocket bridge (/v1/bridge).
## Launched by cklauncher with --server-url=http://127.0.0.1:<port> --bridge-token=<token>.

const VersionInfo = preload("res://addons/corekiosk/ck_bridge_version.gd")

signal connected_to_server(hello: Dictionary)
signal disconnected_from_server()
signal config_updated(values: Dictionary, runtime_values: Dictionary, changed: Array, revision: int)
signal round_armed(round_data: Dictionary)
signal round_cancelled(round_id: String, reason: String)
signal submission_received(submission_id: String, fields: Dictionary)
signal action_triggered(action_id: String, params: Dictionary, call_id: String)
signal board_row_removed(board_id: String, entry_id: String)
signal board_snapshot_received(board_id: String, rows: Array)

@export var game_name: String = "kiosk-game"
@export var game_version: String = "0.1.0"
@export var default_server_url: String = "http://127.0.0.1:8080"

var server_url: String = ""
var bridge_token: String = ""
var capabilities: Dictionary = {}
var values: Dictionary = {}
var runtime_values: Dictionary = {}
var config_revision: int = 0

var _ws := WebSocketPeer.new()
var _connected := false
var _reconnect_timer := 0.0

func _ready() -> void:
	server_url = default_server_url
	for arg in OS.get_cmdline_args():
		if arg.begins_with("--server-url="):
			server_url = arg.substr(13)
		elif arg.begins_with("--bridge-token="):
			bridge_token = arg.substr(15)
	for arg in OS.get_cmdline_user_args():
		if arg.begins_with("--server-url="):
			server_url = arg.substr(13)
		elif arg.begins_with("--bridge-token="):
			bridge_token = arg.substr(15)
	_dial()

func _dial() -> void:
	var ws_url := server_url.replace("https://", "wss://").replace("http://", "ws://").rstrip("/")
	ws_url += "/v1/bridge"
	if not bridge_token.is_empty():
		ws_url += "?token=" + bridge_token.uri_encode()
	_ws = WebSocketPeer.new()
	_ws.connect_to_url(ws_url)

func _process(delta: float) -> void:
	_ws.poll()
	var state := _ws.get_ready_state()
	if state == WebSocketPeer.STATE_OPEN:
		if not _connected:
			_connected = true
			_send_ready()
		while _ws.get_available_packet_count() > 0:
			var raw := _ws.get_packet().get_string_from_utf8()
			var parsed = JSON.parse_string(raw)
			if parsed is Dictionary:
				_handle_message(parsed)
	elif state == WebSocketPeer.STATE_CLOSED:
		if _connected:
			_connected = false
			disconnected_from_server.emit()
		_reconnect_timer += delta
		if _reconnect_timer >= 1.5:
			_reconnect_timer = 0.0
			_dial()

func _send_ready() -> void:
	var msg := {
		"t": "ready",
		"game_name": game_name,
		"game_version": game_version,
		"engine_version": "godot-%s" % Engine.get_version_info().get("string", "4"),
		"bridge_protocol": VersionInfo.CK_PROTOCOL_VERSION,
	}
	if not capabilities.is_empty():
		msg["capabilities"] = capabilities
	send_raw(msg)

func _handle_message(msg: Dictionary) -> void:
	match str(msg.get("t", "")):
		"hello":
			config_revision = int(msg.get("revision", 0))
			values = msg.get("values", {})
			runtime_values = msg.get("runtime_values", {})
			connected_to_server.emit(msg)
			config_updated.emit(values, runtime_values, [], config_revision)
		"config":
			config_revision = int(msg.get("revision", config_revision))
			values = msg.get("values", values)
			runtime_values = msg.get("runtime_values", runtime_values)
			config_updated.emit(values, runtime_values, msg.get("changed", []), config_revision)
		"arm":
			round_armed.emit(msg.get("round", {}))
		"cancel":
			round_cancelled.emit(str(msg.get("round_id", "")), str(msg.get("reason", "")))
		"submission":
			submission_received.emit(str(msg.get("submission_id", "")), msg.get("params", {}))
		"action":
			action_triggered.emit(str(msg.get("action_id", "")), msg.get("params", {}), str(msg.get("call_id", "")))
		"board_row_removed":
			board_row_removed.emit(str(msg.get("board_id", "")), str(msg.get("entry_id", "")))
		"board_snapshot":
			board_snapshot_received.emit(str(msg.get("board_id", "")), msg.get("rows", []))

func send_raw(payload: Dictionary) -> void:
	if _ws.get_ready_state() == WebSocketPeer.STATE_OPEN:
		_ws.send_text(JSON.stringify(payload))

func send_phase(phase_name: String) -> void:
	send_raw({"t": "phase", "phase": phase_name})

func send_score(score: int) -> void:
	send_raw({"t": "score", "score": score})

func send_finished(score: int, reason: String = "completed") -> void:
	send_raw({"t": "finished", "score": score, "reason": reason})

func send_idle() -> void:
	send_raw({"t": "idle"})

func send_config_patch(patch: Dictionary) -> void:
	send_raw({"t": "config_patch", "values": patch})

func send_action_result(call_id: String, action_id: String, ok: bool, message: String = "", data: Dictionary = {}) -> void:
	send_raw({"t": "action_result", "call_id": call_id, "action_id": action_id, "ok": ok, "message": message, "data": data})

func define_board(board_spec: Dictionary) -> void:
	send_raw({"t": "board_define", "board": board_spec})

func upsert_board_entry(board_id: String, entry_id: String, sort_value: float, display: Dictionary, submission_id: String = "") -> void:
	send_raw({
		"t": "board_upsert",
		"board_id": board_id,
		"entry_id": entry_id,
		"submission_id": submission_id,
		"sort_value": sort_value,
		"display": display,
	})
