"""TLS SMTP fixture for hosted end-to-end checks. Delivers only to the test directory."""
from email import policy
from email.parser import BytesParser
import re
import ssl
import subprocess
import time

from aiosmtpd.controller import Controller
from aiosmtpd.smtp import AuthResult, LoginPassword


class Mailbox:
    def __init__(self, directory):
        self.directory = directory

    async def handle_DATA(self, server, session, envelope):
        msg = BytesParser(policy=policy.default).parsebytes(envelope.content)
        if len(envelope.rcpt_tos) != 1 or envelope.rcpt_tos[0] not in ('desktop@example.test', 'mobile@example.test', 'invitee@example.test'):
            return '550 Unrecognized test recipient'
        subject = str(msg['Subject'])
        signin = re.fullmatch(r'(\d{6}) is your Drop sign-in code', subject)
        mailbox = self.directory / envelope.rcpt_tos[0]
        if signin:
            value = signin[1]
        elif subject == 'Verify your Drop Beta waitlist request':
            body = msg.get_body(preferencelist=('plain', 'html'))
            code = re.search(r'\b(\d{6})\b', body.get_content())
            if not code:
                return '550 Missing waitlist verification code'
            value = code[1]
            mailbox = mailbox.with_name(mailbox.name + '.waitlist')
        elif subject == 'Welcome to Drop Beta':
            value = 'received'
            mailbox = mailbox.with_name(mailbox.name + '.welcome')
        else:
            return '550 Unrecognized test message'
        temp = mailbox.with_name(mailbox.name + '.new')
        temp.write_text(value)
        temp.replace(mailbox)
        return '250 OK'


def start_mailbox(root, free_port):
    cert = root / 'cert.pem'
    key = root / 'key.pem'
    subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                    '-subj', '/CN=localhost', '-addext', 'subjectAltName=IP:127.0.0.1',
                    '-keyout', key, '-out', cert], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
    tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.load_cert_chain(cert, key)

    def auth(server, session, envelope, mechanism, credentials):
        valid = isinstance(credentials, LoginPassword) and credentials.login == b'test' and credentials.password == b'test'
        return AuthResult(success=valid, handled=False)

    controller = Controller(Mailbox(root), hostname='127.0.0.1', port=free_port(), tls_context=tls, require_starttls=True,
                      authenticator=auth, auth_required=True)
    controller.start()
    return controller, cert


def read_mail(path, timeout=60):
    deadline = time.monotonic() + timeout
    while not path.is_file():
        if time.monotonic() >= deadline:
            raise AssertionError(f'Missing test email {path.name}')
        time.sleep(0.1)
    return path.read_text()
