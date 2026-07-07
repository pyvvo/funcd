from __future__ import annotations
VERSION = '2.21.2'
from decimal import Decimal
from fastjsonschema import JsonSchemaValueException
NoneType = type(None)

def _funcd_i_validate(data, custom_formats={}, name_prefix=None):
    if not isinstance(data, dict):
        raise JsonSchemaValueException('' + (name_prefix or 'data') + ' must be object', value=data, name='' + (name_prefix or 'data') + '', definition={'properties': {'orderId': {'title': 'Orderid', 'type': 'string'}, 'qty': {'title': 'Qty', 'type': 'integer'}, 'tags': {'items': {'type': 'string'}, 'title': 'Tags', 'type': 'array'}}, 'required': ['orderId', 'qty', 'tags'], 'title': 'FuncInput', 'type': 'object', 'additionalProperties': False}, rule='type')
    data_is_dict = isinstance(data, dict)
    if data_is_dict:
        data__missing_keys = set(['orderId', 'qty', 'tags']) - data.keys()
        if data__missing_keys:
            raise JsonSchemaValueException('' + (name_prefix or 'data') + ' must contain ' + (str(sorted(data__missing_keys)) + ' properties'), value=data, name='' + (name_prefix or 'data') + '', definition={'properties': {'orderId': {'title': 'Orderid', 'type': 'string'}, 'qty': {'title': 'Qty', 'type': 'integer'}, 'tags': {'items': {'type': 'string'}, 'title': 'Tags', 'type': 'array'}}, 'required': ['orderId', 'qty', 'tags'], 'title': 'FuncInput', 'type': 'object', 'additionalProperties': False}, rule='required')
        data_keys = set(data.keys())
        if 'orderId' in data_keys:
            data_keys.remove('orderId')
            data__orderId = data['orderId']
            if not isinstance(data__orderId, str):
                raise JsonSchemaValueException('' + (name_prefix or 'data') + '.orderId must be string', value=data__orderId, name='' + (name_prefix or 'data') + '.orderId', definition={'title': 'Orderid', 'type': 'string'}, rule='type')
        if 'qty' in data_keys:
            data_keys.remove('qty')
            data__qty = data['qty']
            if not isinstance(data__qty, int) and (not (isinstance(data__qty, float) and data__qty.is_integer())) or isinstance(data__qty, bool):
                raise JsonSchemaValueException('' + (name_prefix or 'data') + '.qty must be integer', value=data__qty, name='' + (name_prefix or 'data') + '.qty', definition={'title': 'Qty', 'type': 'integer'}, rule='type')
        if 'tags' in data_keys:
            data_keys.remove('tags')
            data__tags = data['tags']
            if not isinstance(data__tags, (list, tuple)):
                raise JsonSchemaValueException('' + (name_prefix or 'data') + '.tags must be array', value=data__tags, name='' + (name_prefix or 'data') + '.tags', definition={'items': {'type': 'string'}, 'title': 'Tags', 'type': 'array'}, rule='type')
            data__tags_is_list = isinstance(data__tags, (list, tuple))
            if data__tags_is_list:
                data__tags_len = len(data__tags)
                for data__tags_x, data__tags_item in enumerate(data__tags):
                    if not isinstance(data__tags_item, str):
                        raise JsonSchemaValueException('' + (name_prefix or 'data') + '.tags[{data__tags_x}]'.format(**locals()) + ' must be string', value=data__tags_item, name='' + (name_prefix or 'data') + '.tags[{data__tags_x}]'.format(**locals()) + '', definition={'type': 'string'}, rule='type')
        if data_keys:
            raise JsonSchemaValueException('' + (name_prefix or 'data') + ' must not contain ' + str(data_keys) + ' properties', value=data, name='' + (name_prefix or 'data') + '', definition={'properties': {'orderId': {'title': 'Orderid', 'type': 'string'}, 'qty': {'title': 'Qty', 'type': 'integer'}, 'tags': {'items': {'type': 'string'}, 'title': 'Tags', 'type': 'array'}}, 'required': ['orderId', 'qty', 'tags'], 'title': 'FuncInput', 'type': 'object', 'additionalProperties': False}, rule='additionalProperties')
    return data

def __funcd_validate_input(d):
    try:
        _funcd_i_validate(d)
        return []
    except JsonSchemaValueException as e:
        return [str(e)]
from typing import TypedDict

def handle(ctx, event):
    return {'accepted': True}