from fastapi.testclient import TestClient


def test_health_endpoints_answer(client: TestClient) -> None:
    assert client.get("/healthz").json() == {"status": "ok"}
    assert client.get("/readyz").json() == {"status": "ready"}


def test_items_round_trip(client: TestClient) -> None:
    created = client.post("/items", json={"name": "first"})
    assert created.status_code == 201
    assert client.get("/items").json() == [created.json()]
