"""Shapes the requests_instance.yaml queries themselves must reject."""
import requests

# Not a Session() construction — does not match requests_session_instance_binding.
Session = requests.Session

# Non-HTTP-verb method name — does not match requests_session_call.
s.close()
s.mount("https://", adapter)

# No URL argument — does not match requests_session_call.
s.get()
