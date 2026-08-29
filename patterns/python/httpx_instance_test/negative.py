"""Shapes the httpx_instance.yaml queries themselves must reject."""
import httpx

# Client() with no base_url kwarg — does not match httpx_client_instance_binding.
c = httpx.Client(timeout=5.0)

# Non-HTTP-verb method name — does not match httpx_client_call.
c.close()
c.build_request("GET", "/users")

# No URL argument — does not match httpx_client_call.
c.get()
