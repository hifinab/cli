from hifin_template_name import greet


def test_greet() -> None:
    assert greet("world") == "Hello, world!"
