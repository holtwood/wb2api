# NOTICE

## Third-party attribution

This project is released under the MIT License, but portions are derived from
or reference the following MIT-licensed works:

### CLIProxyAPI
- Project: https://github.com/router-for-me/CLIProxyAPI (MIT License)
- Used as: the plugin host platform and SDK (sdk/pluginabi, sdk/pluginapi).
- Our plugin (cmd/plugin) imports the SDK and follows its C ABI contract.

### workbuddy-cliproxy
- Project: https://github.com/lovingfish/workbuddy-cliproxy (MIT License)
- Author: lovingfish (clean-room rebuild; original workbuddy by Sliverkiss)
- Used as: the reference for the plugin C ABI shell shape (cliproxy_plugin_init /
  cliproxyPluginCall/Free/Shutdown, host.stream.emit RPC envelope, registration
  structure) and for the WorkBuddy protocol baseline documented in notes/.
- Our implementation reimplements the protocol logic in wb2api/core from the
  confirmed facts in notes/ and specs/, and the plugin shell follows the ABI
  patterns of this reference.

MIT License text is available at https://opensource.org/licenses/MIT.
