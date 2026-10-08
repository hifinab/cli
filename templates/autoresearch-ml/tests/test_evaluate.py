import time
from pathlib import Path

import pytest
import torch
from torch import nn

import prepare
from evaluate import EvaluationError, check_source, run
from tests.test_prepare import Uniform

CPU = torch.device("cpu")
DATA = torch.randint(0, 20, (prepare.BLOCK * 4 + 1,))


def test_a_run_reports_the_score() -> None:
    result = run(lambda *_: Uniform(20), DATA, DATA, 20, budget=1.0, device=CPU)
    assert result["bpc"] == pytest.approx(4.321928, abs=1e-5)  # log2(20)
    assert result["training_seconds"] < 1.0


def test_training_past_the_budget_fails() -> None:
    def slow(*_: object) -> nn.Module:
        time.sleep(0.5)
        return Uniform(20)

    with pytest.raises(EvaluationError, match="budget"):
        run(slow, DATA, DATA, 20, budget=0.1, device=CPU, grace=0.1)


def test_a_model_that_returns_nan_fails() -> None:
    class Broken(Uniform):
        def forward(self, ids: torch.Tensor) -> torch.Tensor:
            return torch.full((*ids.shape, self.vocab_size), float("nan"))

    with pytest.raises(EvaluationError, match="aren't numbers"):
        run(lambda *_: Broken(20), DATA, DATA, 20, budget=1.0, device=CPU)


def test_train_must_return_a_model() -> None:
    with pytest.raises(EvaluationError, match=r"nn\.Module"):
        run(lambda *_: "a model", DATA, DATA, 20, budget=1.0, device=CPU)  # type: ignore[arg-type]


@pytest.mark.parametrize(
    "source",
    [
        "import os\n",
        "import prepare\n",
        "from pathlib import Path\n",
        "import torch, numpy\n",
        "x = open('eval/val.txt').read()\n",
        "import torch\nstate = torch.load('model.pt')\n",
        "import torch\nmodel = torch.hub.load('a', 'b')\n",
        "exec('import os')\n",
    ],
)
def test_train_files_that_could_read_other_data_are_refused(source: str) -> None:
    with pytest.raises(EvaluationError):
        check_source(source)


def test_the_example_train_is_allowed() -> None:
    check_source(Path("train.py").read_text())
