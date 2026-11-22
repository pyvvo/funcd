# The addon-rauthy lane's probe of rauthy's password policy (ADR-0341): it prints MIN=<n>, the shortest password rauthy
# takes now, which a password change of a user that rauthy holds names when it is too short. It signs in with the lane's
# API key settings. With the argument loose, it first puts a looser policy at rauthy's own API, the drift the settings
# Function puts back.
import json
import re
import subprocess
import sys
import urllib.error
import urllib.request

KEY = "API-Key settings$LaneSettingsKey0addonRauthy0settingsFunction0testValue0000000001"
out = subprocess.run(["funcdctl", "get", "identityproviders", "login", "-n", "default", "-o", "json"],
                     capture_output=True, text=True, check=True).stdout
addr = json.loads(out)["status"]["address"]


def call(method, path, body=None):
    req = urllib.request.Request("http://" + addr + path, data=None if body is None else json.dumps(body).encode(),
                                 method=method, headers={"Authorization": KEY, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            return r.status, json.loads(r.read() or b"null")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


if sys.argv[1:] == ["loose"]:
    st, _ = call("PUT", "/auth/v1/password_policy", {"length_min": 10, "length_max": 128, "include_lower_case": None,
                                                     "include_upper_case": None, "include_digits": None,
                                                     "include_special": None, "valid_days": None,
                                                     "not_recently_used": None})
    print("LOOSE", st)
st, users = call("GET", "/auth/v1/users")
user = next((u for u in users if u.get("email") == "probe@login.example.com"), None) if st == 200 else None
if user is None:
    st, user = call("POST", "/auth/v1/users", {"email": "probe@login.example.com", "given_name": "Probe",
                                               "language": "en", "roles": []})
st, body = call("PUT", "/auth/v1/users/" + user["id"], {
    "email": user["email"], "given_name": user.get("given_name"), "family_name": user.get("family_name"),
    "language": "en", "password": "aA1!bC2@", "roles": user.get("roles") or [], "groups": None, "enabled": True,
    "email_verified": False, "user_expires": None, "user_values": None})
m = re.search(r"Minimum password length is (\d+)", str(body))
print("MIN=" + (m.group(1) if st == 400 and m else "?"), st, str(body)[:200])
