mgw-connectlife-dc
==================

Device connector for the multi-gateway. Connects ConnectLife appliances (Hisense, Gorenje, ASKO) through the ConnectLife cloud.

The module definition lives in [mgw-module-repository](https://github.com/SENERGY-Platform/mgw-module-repository), `main/connectlife`.

How it works
------------

- Polls the appliance list of the ConnectLife account every `POLL_INTERVAL` (default 1 minute).
- Each appliance becomes an mgw device with the id `connectlife-<deviceId>`. `offlineState` decides online or offline. An appliance that leaves the account is reported offline, not deleted.
- Device types are generated per appliance model from the property list of the model (`get_property_list`, available while the appliance is offline) and its status list, identified by the attribute `connectlife/type` = `<deviceTypeCode>-<deviceFeatureCode>`. On first sight the connector searches the platform for a device type with that attribute and creates one if none exists. Creation sets `distinct_attributes=connectlife/type`, so two gateways can not create duplicates.
- Platform requests go to the api gateway of the mgw cloud proxy, which authenticates with the client certificate of the gateway.

Property names differ between models, also within one appliance class. Tumble dryers, for example, report under type codes `004`, `030` and `032` with different property sets. The cloud describes every model itself, including which properties are writable, so no per-model mapping is needed.

Services of a generated device type
-----------------------------------

| Local id | Interaction | Content |
|---|---|---|
| `status` | event+request | Structure `status` with every property of the model. A property with a reported value gets its type (numbers, sent as strings, are `Float`, all others `Text`); one known only from the property list is `Float`. |
| `set_property` | request | Structure `command` with `property` and `value`. Writes one property. The service description lists the writable properties; the connector rejects writes to the others. |

The content variables carry no characteristics or functions, so the device types are usable in device views, exports and analytics, but not in process modelling by function. Properties that appear in the status list after the device type was created are not forwarded; the connector logs a warning once per device. A text value for a property typed `Float` is dropped.

Configuration
-------------

| Variable | Default | Meaning |
|---|---|---|
| `CONNECTLIFE_USERNAME`, `CONNECTLIFE_PASSWORD` | | ConnectLife account |
| `CONNECTLIFE_GIGYA_API_KEY` | value of the app | API key of the ConnectLife app at Gigya |
| `CONNECTLIFE_OAUTH_CLIENT_SECRET` | value of the app | OAuth client secret of the ConnectLife app |
| `CONNECTLIFE_APP_SECRET` | value of the app | `appSecret` of the ConnectLife app at the HijuConn gateway |
| `CONNECTLIFE_SIGN_SALT` | value of the app | Suffix appended before the request signature is hashed |
| `POLL_INTERVAL` | `1m` | Poll interval |
| `MGW_MQTT_BROKER` | `tcp://localhost:1883` | mgw broker |
| `PLATFORM_API_URL` | `http://gateway` | api gateway of the mgw cloud proxy |
| `PLATFORM_PROTOCOL_ID`, `PLATFORM_PROTOCOL_SEGMENT_ID` | standard-connector, segment `data` | Protocol of generated services |
| `DEVICE_ID_PREFIX` | `connectlife-` | Prefix of local device ids |
| `LOG_LEVEL` | `info` | |

The four app values belong to the ConnectLife app and are the same for every account; the defaults are the values the Python library [connectlife](https://github.com/oyvindwe/connectlife) uses. If the app changes them, login fails until the variables are set to the new values.

Protocol
--------

The ConnectLife protocol follows the description of the app API as used by the Home Assistant integration [connectlife-ha](https://github.com/oyvindwe/connectlife-ha). Both projects are GPL-3.0; this connector is an independent implementation and contains no code or mapping files from them.

1. `POST accounts.eu1.gigya.com/accounts.login` with account and Gigya API key, returns `UID` and `sessionInfo.cookieValue`.
2. `POST accounts.eu1.gigya.com/accounts.getJWT`, returns `id_token`.
3. `POST oauth.hijuconn.com/oauth/authorize` with `id_token` and `UID`, returns `code`.
4. `POST oauth.hijuconn.com/oauth/token`, returns access and refresh token.
5. Signed requests to `clife-eu-gateway.hijuconn.com`: `GET /clife-svc/pu/get_device_status_list`, `POST /device/pu/property/set`. The signature is the SHA-256 of all fields sorted by key, joined as `key=value` with `&` and followed by the salt, encrypted with the RSA key of the app (PKCS#1 v1.5) and base64-encoded.

Trying it against the cloud
---------------------------

`scripts/try-connectlife.sh` asks for the account once, stores them in `.local/connectlife.env` (git-ignored, mode 600) and runs the CLI read-only: login, appliances, properties per appliance, property metadata of the model, and the device types that would be created. With `credctl` it also looks up existing device types on the platform. Results land in `.local/out/<timestamp>/`.

Single functions: `go run ./cmd/connectlife-cli` with the same environment variables as the connector. `set <device> <property> <value>` is the only command that writes, and the script never calls it.
