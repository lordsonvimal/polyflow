# No class / module declaration and no `self.table_name =` assignment, so none
# of the model_class / model_table_name / model_abstract patterns may match.
CONFIG = { retries: 3 }.freeze

def retry_count
  CONFIG[:retries]
end
