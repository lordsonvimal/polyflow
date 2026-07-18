"""No Celery patterns — plain decorators and regular function/class definitions.

Intentionally has NO .delay() / .apply_async() calls and NO @X.task /
@shared_task decorators, so that the fixture test confirms zero false positives
when the celery package gate is bypassed (as fixture tests do not apply
version gating).
"""


def process(item):
    return item.strip()


def send(message, recipient):
    """send is a plain function, not a task."""
    return {"message": message, "to": recipient}


class Worker:
    def run(self):
        return self.execute()

    def execute(self):
        pass

    def dispatch(self, job_id):
        """dispatch is a regular method, not a Celery dispatch."""
        return job_id


@staticmethod
def format_data(data):
    return str(data)


@property
def status(self):
    return self._status
