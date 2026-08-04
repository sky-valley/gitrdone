package judgement

import "errors"

type PendingService interface {
	ConcernService
	PromotionService
}

type PendingProcessorFactory interface {
	Build(service PendingService, content RepositoryContent) (PendingProcessor, error)
}

type concernProcessorFactory struct {
	evaluator ConcernEvaluator
}

func NewConcernProcessorFactory(evaluator ConcernEvaluator) PendingProcessorFactory {
	return concernProcessorFactory{evaluator: evaluator}
}

func (factory concernProcessorFactory) Build(service PendingService, content RepositoryContent) (PendingProcessor, error) {
	if factory.evaluator == nil {
		return nil, errors.New("concern processor factory requires an evaluator")
	}
	inputs, err := NewRepositoryAssessmentInputSource(content)
	if err != nil {
		return nil, err
	}
	return NewConcernProcessor(service, factory.evaluator, inputs)
}

type approveAllProcessorFactory struct{}

// NewApproveAllProcessorFactory keeps the temporary migration behavior
// explicit at composition sites. Production judgement must choose its own
// processor rather than falling back to approval.
func NewApproveAllProcessorFactory() PendingProcessorFactory {
	return approveAllProcessorFactory{}
}

func (approveAllProcessorFactory) Build(service PendingService, _ RepositoryContent) (PendingProcessor, error) {
	if service == nil {
		return nil, errors.New("approve-all processor factory requires a service")
	}
	return NewApproveAllProcessor(service), nil
}
