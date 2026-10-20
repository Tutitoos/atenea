import unittest
from acceptance import Bridge, Rejected


class ErrorReceiptTests(unittest.TestCase):
    def test_mcp_cancellation_preserves_message_and_kind(self):
        bridge = Bridge.__new__(Bridge)
        bridge.transport = 'mcp'
        bridge.request = lambda *_: {
            'isError': True,
            'structuredContent': {'error_code': 'canceled'},
            'content': [{'type': 'text', 'text': 'Stopped after human input; observe before continuing.'}],
        }
        with self.assertRaises(Rejected) as caught:
            bridge.call('drag', {})
        self.assertEqual(caught.exception.payload['kind'], 'canceled')
        self.assertIn('human input', caught.exception.payload['error'])

    def test_helper_refusal_keeps_its_existing_kind(self):
        bridge = Bridge.__new__(Bridge)
        bridge.transport = 'helper'
        bridge.request = lambda *_: {
            'isError': True,
            'content': [{'type': 'text', 'text': '{"kind":"denied","error":"Secure field refused"}'}],
        }
        with self.assertRaises(Rejected) as caught:
            bridge.call('type', {})
        self.assertEqual(caught.exception.payload['kind'], 'denied')
        self.assertIn('Secure field', caught.exception.payload['error'])

    def test_helper_cancellation_text_keeps_kind(self):
        bridge = Bridge.__new__(Bridge)
        bridge.transport = 'helper'
        bridge.request = lambda *_: {
            'isError': True,
            'content': [{'type': 'text', 'text': '{"kind":"canceled","error":"Operation canceled"}'}],
        }
        with self.assertRaises(Rejected) as caught:
            bridge.call('drag', {})
        self.assertEqual(caught.exception.payload['kind'], 'canceled')


if __name__ == '__main__':
    unittest.main()
