"""Official SDK checks against the Go test fixture's real HTTP/SSE handlers."""
import argparse
import json
import unittest

import anthropic
import openai
from openai.types.responses import Response, CompactedResponse

parser = argparse.ArgumentParser()
parser.add_argument("--base-url", required=True)
args = parser.parse_args()
MODEL = "gpt-5.6-reasoning"
SCHEMA = {"type": "object", "properties": {"board": {"type": "string"}}, "required": ["board"]}


class Contracts(unittest.TestCase):
    def setUp(self):
        self.ai = openai.OpenAI(api_key="sdk-fixture-key", base_url=args.base_url, max_retries=0)
        self.anthropic = anthropic.Anthropic(api_key="sdk-fixture-key", base_url=args.base_url.removesuffix("/v1"), max_retries=0)

    def tearDown(self):
        self.ai.close()
        self.anthropic.close()

    def test_chat_stream(self):
        chunks = list(self.ai.chat.completions.create(model=MODEL, messages=[{"role": "user", "content": "SDK_TEXT"}], stream=True))
        self.assertEqual("".join(c.choices[0].delta.content or "" for c in chunks if c.choices), "Board ready: café")
        self.assertEqual(chunks[-1].choices[0].finish_reason, "stop")

    def test_empty_probe_lifecycle(self):
        with self.ai.responses.stream(model=MODEL, input=[]) as stream:
            events = list(stream)
            final = stream.get_final_response()
        self.assertEqual(final.output, [])
        self.assertEqual(events[0].response.status, "in_progress")
        self.assertFalse(final.store)
        with self.assertRaises(openai.NotFoundError):
            self.ai.responses.retrieve(final.id)

    def test_model_reused_tool_ids_do_not_collide_in_history(self):
        tools = [{"type": "function", "name": "read_board", "parameters": SCHEMA}]
        first = self.ai.responses.create(model=MODEL, input="SDK_TOOL", tools=tools)
        call = next(item for item in first.output if item.type == "function_call")
        second = self.ai.responses.create(model=MODEL, previous_response_id=first.id, tools=tools, input=[
            {"type": "function_call_output", "call_id": call.call_id, "output": "first board read"},
            {"role": "user", "content": "SDK_TOOL again after the change"},
        ])
        next_call = next(item for item in second.output if item.type == "function_call")
        self.assertNotEqual(call.call_id, next_call.call_id)
        final = self.ai.responses.create(model=MODEL, previous_response_id=second.id, tools=tools, input=[
            {"type": "function_call_output", "call_id": next_call.call_id, "output": "SDK_RESULT: second read"},
        ])
        self.assertIn("CLIENT_RESULT_ACCEPTED", final.output_text)

    def test_responses_stream_lifecycle(self):
        with self.ai.responses.stream(model=MODEL, input="SDK_TEXT") as stream:
            events = list(stream)
            final = stream.get_final_response()
        self.assertEqual(final.output_text, "Board ready: café")
        self.assertEqual(final.status, "completed")
        wire = [event for event in events if hasattr(event, "sequence_number")]
        self.assertEqual([event.sequence_number for event in wire], list(range(len(wire))))
        Response.model_validate(final.model_dump())

    def test_late_reasoning_keeps_item_indexes_and_snapshot_consistent(self):
        with self.ai.responses.stream(model=MODEL, input="SDK_LATE_REASONING") as stream:
            events = list(stream)
            final = stream.get_final_response()
        added = [event for event in events if event.type == "response.output_item.added"]
        self.assertEqual([event.output_index for event in added], list(range(len(added))))
        done = [event for event in events if event.type == "response.output_item.done"]
        ordered = sorted(done, key=lambda event: event.output_index)
        self.assertEqual([event.item.id for event in ordered], [item.id for item in final.output])
        self.assertEqual(final.output_text, "Board ready: café")

    def test_responses_tool_result(self):
        tools = [{"type": "function", "name": "read_board", "parameters": SCHEMA}]
        first = self.ai.responses.create(model=MODEL, input="SDK_TOOL", tools=tools)
        fetched = self.ai.responses.retrieve(first.id)
        self.assertEqual(fetched.output, first.output)
        call = next(item for item in first.output if item.type == "function_call")
        self.assertEqual(json.loads(call.arguments), {"board": "alpha"})
        # Only the client supplies/executed this result. The bridge must carry it.
        second = self.ai.responses.create(model=MODEL, input=[
            {"role": "user", "content": "SDK_TOOL"}, call.model_dump(exclude_none=True),
            {"type": "function_call_output", "call_id": call.call_id, "output": "SDK_RESULT: board alpha"},
        ], tools=tools)
        self.assertIn("CLIENT_RESULT_ACCEPTED", second.output_text)

    def test_responses_stream_tool_lifecycle(self):
        tools = [{"type": "function", "name": "read_board", "parameters": SCHEMA}]
        with self.ai.responses.stream(model=MODEL, input="SDK_TOOL", tools=tools, parallel_tool_calls=False) as stream:
            events = list(stream)
            final = stream.get_final_response()
        Response.model_validate(final.model_dump())
        self.assertFalse(final.parallel_tool_calls)
        calls = [item for item in final.output if item.type == "function_call"]
        self.assertEqual(len(calls), 1)
        added = next(e for e in events if e.type == "response.output_item.added" and e.item.type == "function_call")
        done = next(e for e in events if e.type == "response.output_item.done" and e.item.type == "function_call")
        self.assertEqual(added.item.id, done.item.id)
        self.assertEqual(done.item.call_id, calls[0].call_id)
        arguments = "".join(e.delta for e in events if e.type == "response.function_call_arguments.delta")
        self.assertEqual(arguments, calls[0].arguments)
        self.assertEqual(json.loads(arguments), {"board": "alpha"})

    def test_anthropic_tool_round_trip(self):
        tools = [{"name": "read_board", "input_schema": SCHEMA}]
        with self.anthropic.messages.stream(model=MODEL, max_tokens=512, tools=tools, messages=[{"role": "user", "content": "SDK_TOOL"}]) as stream:
            list(stream)
            first = stream.get_final_message()
        call = next(item for item in first.content if item.type == "tool_use")
        self.assertEqual(call.input, {"board": "alpha"})
        result = self.anthropic.messages.create(model=MODEL, max_tokens=512, tools=tools, messages=[
            {"role": "user", "content": "SDK_TOOL"}, {"role": "assistant", "content": [item.model_dump(exclude_none=True) for item in first.content]},
            {"role": "user", "content": [{"type": "tool_result", "tool_use_id": call.id, "content": "SDK_RESULT: board alpha"}]},
        ])
        self.assertEqual(result.stop_reason, "end_turn")
        self.assertIn("CLIENT_RESULT_ACCEPTED", "".join(item.text for item in result.content if item.type == "text"))

    def test_compaction_schema(self):
        result = self.ai.responses.compact(model=MODEL, input=[{"role": "user", "content": "SDK_COMPACT: preserve board tasks"}])
        CompactedResponse.model_validate(result.model_dump())
        self.assertEqual(result.object, "response.compaction")
        self.assertTrue(result.output[0].encrypted_content.startswith("m365cp1."))

    def test_truncated_compaction_is_rejected(self):
        with self.assertRaises(openai.BadRequestError):
            self.ai.responses.compact(model=MODEL, input=[{"role": "user", "content": "SDK_COMPACT"}], extra_body={"max_output_tokens": 1})
        with self.ai.responses.stream(model=MODEL, input=[{"role": "user", "content": "SDK_COMPACT"}, {"type": "compaction_trigger"}], max_output_tokens=1) as stream:
            events = list(stream)
        self.assertTrue(any(e.type == "response.failed" for e in events))
        self.assertFalse(any(e.type == "response.completed" for e in events))

    def test_previous_response_id_and_store_false(self):
        tools = [{"type": "function", "name": "read_board", "parameters": SCHEMA}]
        first = self.ai.responses.create(model=MODEL, input="SDK_TOOL", tools=tools)
        call = next(item for item in first.output if item.type == "function_call")
        second = self.ai.responses.create(model=MODEL, previous_response_id=first.id, input=[
            {"type": "function_call_output", "call_id": call.call_id, "output": "SDK_RESULT: board alpha"},
        ], tools=tools)
        self.assertIn("CLIENT_RESULT_ACCEPTED", second.output_text)
        other = openai.OpenAI(api_key="sdk-other-key", base_url=args.base_url, max_retries=0)
        try:
            with self.assertRaises(openai.NotFoundError):
                other.responses.create(model=MODEL, previous_response_id=first.id, input="continue")
        finally:
            other.close()
        private = self.ai.responses.create(model=MODEL, input="SDK_TEXT", store=False)
        with self.assertRaises(openai.NotFoundError):
            self.ai.responses.create(model=MODEL, previous_response_id=private.id, input="continue")

    def test_compaction_preserves_a_pending_tool_call(self):
        compact = self.ai.responses.compact(model=MODEL, input=[
            {"role": "user", "content": "SDK_COMPACT"},
            {"type": "function_call", "call_id": "pending-read", "name": "read_board", "arguments": '{"board":"alpha"}'},
        ])
        result = self.ai.responses.create(model=MODEL, input=[
            *[item.model_dump(exclude_none=True) for item in compact.output],
            {"type": "function_call_output", "call_id": "pending-read", "output": "SDK_RESULT: board alpha"},
        ])
        self.assertIn("CLIENT_RESULT_ACCEPTED", result.output_text)

    def test_tasks_survive_failure_and_compaction(self):
        headers = {"Session-Id": "sdk-board-checkpoint"}
        tools = [{"type": "function", "name": "read_board", "parameters": SCHEMA}]
        self.ai.responses.create(model=MODEL, extra_headers=headers, tools=tools, input=[
            {"role": "user", "content": "SDK_TOOL"},
            {"type": "function_call", "call_id": "plan-board", "name": "update_plan", "arguments": json.dumps({"plan": [
                {"step": "implement board", "status": "in_progress"}, {"step": "verify WIP", "status": "pending"}]})},
            {"type": "function_call_output", "call_id": "plan-board", "output": "Plan updated"},
        ])
        with self.assertRaises(openai.APIStatusError):
            self.ai.responses.create(model=MODEL, extra_headers=headers, input="SDK_FAILURE")
        compact = self.ai.responses.compact(model=MODEL, extra_headers=headers, input=[{"role": "user", "content": "SDK_COMPACT"}])
        # store=False and no session header force restoration from the client-held
        # capsule, rather than relying on the server's session checkpoint.
        result = self.ai.responses.create(model=MODEL, store=False, tools=tools, input=[
            *[item.model_dump(exclude_none=True) for item in compact.output],
            {"role": "user", "content": "SDK_CHECK_TASKS"},
        ])
        self.assertTrue(any(item.type == "function_call" for item in result.output))

    def test_incomplete_stream_and_response_delete(self):
        with self.ai.responses.stream(model=MODEL, input="SDK_TEXT", max_output_tokens=1) as stream:
            events = list(stream)
        # This SDK's convenience getter only accepts response.completed; the
        # protocol exposes a truncated turn through response.incomplete instead.
        final = next(event.response for event in events if event.type == "response.incomplete")
        Response.model_validate(final.model_dump())
        self.assertEqual(final.status, "incomplete")
        self.assertTrue(any(e.type == "response.incomplete" for e in events))
        self.assertFalse(any(e.type == "response.completed" for e in events))
        self.ai.responses.delete(final.id)
        with self.assertRaises(openai.NotFoundError):
            self.ai.responses.retrieve(final.id)

    def test_in_band_compaction_is_a_responses_object(self):
        result = self.ai.responses.create(model=MODEL, input=[{"role": "user", "content": "SDK_COMPACT"}, {"type": "compaction_trigger"}])
        self.assertEqual(result.object, "response")
        self.assertEqual(result.output[-1].type, "compaction")

    def test_in_band_streaming_compaction(self):
        with self.ai.responses.stream(model=MODEL, input=[{"role": "user", "content": "SDK_COMPACT"}, {"type": "compaction_trigger"}]) as stream:
            events = list(stream)
            final = stream.get_final_response()
        Response.model_validate(final.model_dump())
        self.assertEqual(final.object, "response")
        done = next(e for e in events if e.type == "response.output_item.done")
        self.assertEqual(done.item, final.output[0])
        self.assertTrue(done.item.encrypted_content.startswith("m365cp1."))

    def test_disconnect_after_partial_text_is_not_completion(self):
        with self.ai.responses.stream(model=MODEL, input="SDK_DISCONNECT") as stream:
            events = list(stream)
        self.assertTrue(any(e.type == "response.output_text.delta" for e in events))
        self.assertTrue(any(e.type == "response.failed" for e in events))
        self.assertFalse(any(e.type == "response.completed" for e in events))

    def test_anthropic_stream(self):
        with self.anthropic.messages.stream(model=MODEL, max_tokens=128, messages=[{"role": "user", "content": "SDK_TEXT"}]) as stream:
            text = "".join(stream.text_stream)
            final = stream.get_final_message()
        self.assertEqual(text, "Board ready: café")
        self.assertEqual(final.stop_reason, "end_turn")

    def test_errors_are_not_assistant_answers(self):
        with self.assertRaises(openai.APIStatusError):
            self.ai.responses.create(model=MODEL, input="SDK_FAILURE")
        with self.ai.responses.stream(model=MODEL, input="SDK_FAILURE") as stream:
            events = list(stream)
        self.assertTrue(any(e.type == "response.failed" for e in events))
        self.assertFalse(any(e.type == "response.completed" for e in events))
        self.assertNotIn("must-not-escape", str(events))


if __name__ == "__main__":
    unittest.main(argv=[__file__], verbosity=2)
