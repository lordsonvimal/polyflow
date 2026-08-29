"""Non-Django-ORM call sites that superficially resemble the ORM patterns."""


def use_manager():
    return SomeClass.manager.filter(x=1)


def call_get_bare():
    return response.get("key")


def call_filter_on_list():
    return items.filter(lambda x: x > 0)
