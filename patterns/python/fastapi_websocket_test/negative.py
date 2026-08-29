"""Unrelated method calls that must not match the WS pump patterns."""


def poll(queue):
    return queue.get()


def notify(logger):
    logger.info("done")


def process(config):
    return config.fetch()
