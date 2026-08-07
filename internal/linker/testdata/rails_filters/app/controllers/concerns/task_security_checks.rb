# frozen_string_literal: true

module TaskSecurityChecks
  def can_manage_task_for_study?(study)
    head :forbidden unless study.manageable_by?(current_user)
  end
end
